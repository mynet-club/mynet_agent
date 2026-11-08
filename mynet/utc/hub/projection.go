package hub

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

const (
	CapabilityEpoch            uint32 = 1 << 0
	CapabilityExplicitActivate uint32 = 1 << 1
	CapabilityMask                    = CapabilityEpoch | CapabilityExplicitActivate
	SchemaVersion                     = 2
	maxPeers                          = 128
)

type Peer struct {
	PeerID         uint64
	PairID         uint64
	PairGen        uint64
	Mode           uint8
	LeaseEpoch     uint64
	PlacementEpoch uint64
	ExpiresAt      uint64
	Family         uint8
	Address        [16]byte
	Port           uint16
	ScopeID        uint32
	Manifest       string
}

type Projection struct {
	Scope        string
	Instance     string
	LocalID      uint64
	ConfigGen    uint64
	BootID       string
	PID          uint64
	Capabilities uint32
	Peers        []Peer
}

func token(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

func validBootID(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 16 && hex.EncodeToString(raw) == value
}

func (peer Peer) validate(localID uint64) error {
	if peer.PeerID == 0 || peer.PeerID == localID || peer.PairID == 0 ||
		peer.PairGen == 0 || peer.LeaseEpoch == 0 || peer.PlacementEpoch == 0 {
		return fmt.Errorf("invalid Hub peer identity")
	}
	digest, err := hex.DecodeString(peer.Manifest)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != peer.Manifest {
		return fmt.Errorf("invalid Hub manifest digest")
	}
	switch peer.Mode {
	case 0:
		if peer.Family != 0 || peer.Port != 0 || peer.ScopeID != 0 {
			return fmt.Errorf("direct peer has export fields")
		}
		for _, b := range peer.Address {
			if b != 0 {
				return fmt.Errorf("direct peer has export address")
			}
		}
	case 1:
		if peer.ExpiresAt == 0 || peer.Port == 0 ||
			(peer.Family != 4 && peer.Family != 6) {
			return fmt.Errorf("invalid export peer")
		}
		address := netip.AddrFrom16(peer.Address)
		if peer.Family == 4 {
			if peer.ScopeID != 0 || !address.Is4In6() ||
				peer.Address[10] != 0xff || peer.Address[11] != 0xff {
				return fmt.Errorf("invalid IPv4 export scope/address")
			}
			for i := 0; i < 10; i++ {
				if peer.Address[i] != 0 {
					return fmt.Errorf("IPv4 export address is not zero-padded")
				}
			}
		} else if address.Zone() != "" || address.Is4In6() {
			return fmt.Errorf("invalid IPv6 export address")
		}
	default:
		return fmt.Errorf("unsupported Hub mode")
	}
	return nil
}

func (p Projection) Canonical() ([]byte, error) {
	if !token(p.Scope) || !token(p.Instance) || p.LocalID == 0 || p.ConfigGen == 0 ||
		!validBootID(p.BootID) || p.PID == 0 || p.Capabilities&CapabilityMask == 0 ||
		p.Capabilities&^CapabilityMask != 0 || len(p.Peers) > maxPeers {
		return nil, fmt.Errorf("invalid Hub projection header")
	}
	peers := append([]Peer(nil), p.Peers...)
	sort.Slice(peers, func(i, j int) bool { return peers[i].PeerID < peers[j].PeerID })
	pairs := make(map[uint64]bool, len(peers))
	var b strings.Builder
	fmt.Fprintf(&b, "utc-v2 %s %s %d %d %s %d %d\n",
		p.Scope, p.Instance, p.LocalID, p.ConfigGen, p.BootID, p.PID, p.Capabilities)
	for i, peer := range peers {
		if err := peer.validate(p.LocalID); err != nil {
			return nil, err
		}
		if pairs[peer.PairID] || i > 0 && peers[i-1].PeerID == peer.PeerID {
			return nil, fmt.Errorf("duplicate Hub pair or peer")
		}
		pairs[peer.PairID] = true
		fmt.Fprintf(&b, "peer %d %d %d %d %d %d %d %d %x %d %d %s\n",
			peer.PeerID, peer.PairID, peer.PairGen, peer.Mode, peer.LeaseEpoch,
			peer.PlacementEpoch, peer.ExpiresAt, peer.Family, peer.Address,
			peer.Port, peer.ScopeID, peer.Manifest)
	}
	return []byte(b.String()), nil
}

func parseUint(field string, bits int) (uint64, error) {
	return strconv.ParseUint(field, 10, bits)
}

func Parse(raw []byte) (Projection, error) {
	var projection Projection
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return projection, fmt.Errorf("empty Hub projection")
	}
	header := strings.Fields(lines[0])
	if len(header) != 8 || header[0] != "utc-v2" {
		return projection, fmt.Errorf("invalid Hub projection header")
	}
	var err error
	projection.Scope, projection.Instance = header[1], header[2]
	if projection.LocalID, err = parseUint(header[3], 64); err != nil {
		return projection, err
	}
	if projection.ConfigGen, err = parseUint(header[4], 64); err != nil {
		return projection, err
	}
	projection.BootID = header[5]
	if projection.PID, err = parseUint(header[6], 64); err != nil {
		return projection, err
	}
	caps, err := parseUint(header[7], 32)
	if err != nil {
		return projection, err
	}
	projection.Capabilities = uint32(caps)
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 13 || fields[0] != "peer" {
			return Projection{}, fmt.Errorf("invalid Hub peer row")
		}
		var peer Peer
		if peer.PeerID, err = parseUint(fields[1], 64); err != nil {
			return Projection{}, err
		}
		if peer.PairID, err = parseUint(fields[2], 64); err != nil {
			return Projection{}, err
		}
		if peer.PairGen, err = parseUint(fields[3], 64); err != nil {
			return Projection{}, err
		}
		mode, err := parseUint(fields[4], 8)
		if err != nil {
			return Projection{}, err
		}
		peer.Mode = uint8(mode)
		if peer.LeaseEpoch, err = parseUint(fields[5], 64); err != nil {
			return Projection{}, err
		}
		if peer.PlacementEpoch, err = parseUint(fields[6], 64); err != nil {
			return Projection{}, err
		}
		if peer.ExpiresAt, err = parseUint(fields[7], 64); err != nil {
			return Projection{}, err
		}
		family, err := parseUint(fields[8], 8)
		if err != nil {
			return Projection{}, err
		}
		peer.Family = uint8(family)
		address, err := hex.DecodeString(fields[9])
		if err != nil || len(address) != 16 {
			return Projection{}, fmt.Errorf("invalid Hub address")
		}
		copy(peer.Address[:], address)
		port, err := parseUint(fields[10], 16)
		if err != nil {
			return Projection{}, err
		}
		peer.Port = uint16(port)
		scopeID, err := parseUint(fields[11], 32)
		if err != nil {
			return Projection{}, err
		}
		peer.ScopeID = uint32(scopeID)
		// The last field is manifest; peer has 12 fields total.
		peer.Manifest = fields[len(fields)-1]
		projection.Peers = append(projection.Peers, peer)
	}
	canonical, err := projection.Canonical()
	if err != nil {
		return Projection{}, err
	}
	if !bytes.Equal(canonical, raw) {
		return Projection{}, fmt.Errorf("Hub projection is not canonical")
	}
	return projection, nil
}
