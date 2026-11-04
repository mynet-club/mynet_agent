package driver

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// UTCProjection is a trusted executor projection, not a signed lease. The
// coordinator authorizes operations before calling this local privileged API.
type UTCProjection struct {
	Scope     string          `json:"scope"`
	Instance  string          `json:"instance"`
	LocalID   uint64          `json:"local_id"`
	ConfigGen uint64          `json:"config_gen"`
	Peers     []UTCPeerPolicy `json:"peers"`
}
type UTCPeerPolicy struct {
	PeerID    uint64 `json:"peer_id"`
	PairID    uint64 `json:"pair_id"`
	PairGen   uint64 `json:"pair_gen"`
	Mode      uint8  `json:"mode"`
	ExpiresAt uint64 `json:"expires_at"`
	Export    string `json:"export"` // numeric IP:port, no hostname/zone lookup
	ScopeID   uint32 `json:"scope_id"`
	Manifest  string `json:"manifest"` // common manifest SHA-256, canonical hex
}

func utcToken(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
func (p UTCProjection) canonical() ([]byte, error) {
	if !utcToken(p.Scope) || !utcToken(p.Instance) || p.LocalID == 0 || p.ConfigGen == 0 || len(p.Peers) > 128 {
		return nil, fmt.Errorf("invalid UTC projection scope or capacity")
	}
	peers := append([]UTCPeerPolicy(nil), p.Peers...)
	sort.Slice(peers, func(i, j int) bool { return peers[i].PeerID < peers[j].PeerID })
	var b strings.Builder
	fmt.Fprintf(&b, "utc-v1 %s %s %d %d\n", p.Scope, p.Instance, p.LocalID, p.ConfigGen)
	pairs := map[uint64]bool{}
	for i, peer := range peers {
		if peer.PeerID == 0 || peer.PeerID == p.LocalID || peer.PairID == 0 || peer.PairGen == 0 || pairs[peer.PairID] || i > 0 && peers[i-1].PeerID == peer.PeerID {
			return nil, fmt.Errorf("invalid or duplicate UTC peer/pair")
		}
		pairs[peer.PairID] = true
		digest, err := hex.DecodeString(peer.Manifest)
		if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != peer.Manifest {
			return nil, fmt.Errorf("invalid UTC manifest digest")
		}
		family, port := 0, uint16(0)
		var address [16]byte
		switch peer.Mode {
		case 0:
			if peer.Export != "" || peer.ExpiresAt != 0 || peer.ScopeID != 0 {
				return nil, fmt.Errorf("direct projection has export fields")
			}
		case 1:
			endpoint, err := netip.ParseAddrPort(peer.Export)
			if err != nil || endpoint.Port() == 0 || endpoint.Addr().Zone() != "" || endpoint.Addr().Is4In6() || peer.ExpiresAt == 0 {
				return nil, fmt.Errorf("invalid UTC export")
			}
			port = endpoint.Port()
			if endpoint.Addr().Is4() {
				family = 4
				v := endpoint.Addr().As4()
				copy(address[:4], v[:])
				if peer.ScopeID != 0 {
					return nil, fmt.Errorf("IPv4 export has IPv6 scope")
				}
			} else {
				family = 6
				address = endpoint.Addr().As16()
			}
		default:
			return nil, fmt.Errorf("unsupported UTC mode")
		}
		fmt.Fprintf(&b, "peer %d %d %d %d %d %d %x %d %d %s\n", peer.PeerID, peer.PairID, peer.PairGen, peer.Mode, peer.ExpiresAt, family, address, port, peer.ScopeID, peer.Manifest)
	}
	return []byte(b.String()), nil
}

// durableUTCWrite publishes only after file+directory fsync. A failed rename
// or fsync is an error even when the new content is visible: never report apply.
func durableUTCWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".utc-write-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncUTCHistory(path)
}

// ApplyUTCProjection serializes with existing lifecycle operations. Persisted
// high water precedes utc.conf. It means PUBLISHED, never APPLIED/EFFECTIVE;
// callers still must verify the current GNB boot and per-pair status.
func (d *GNBDriver) ApplyUTCProjection(next UTCProjection) error {
	return d.withLifecycle(func() error { return d.applyUTCProjectionLocked(next) })
}

func (d *GNBDriver) applyUTCProjectionLocked(next UTCProjection) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("UTC GNB execution unsupported on Windows")
	}
	text, err := next.canonical()
	if err != nil {
		return err
	}
	if d.NodeID > 0 && uint64(d.NodeID) != next.LocalID {
		return fmt.Errorf("UTC local node mismatch")
	}
	if err := d.checkUTCFeature(); err != nil {
		return err
	}
	history, _, err := d.utcHistoryPath()
	if err != nil {
		return err
	}
	ledgerPath := history + ".projection"
	info, statErr := os.Lstat(ledgerPath)
	if statErr == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 131072) {
		return fmt.Errorf("UTC projection history must be a bounded private file")
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	oldBytes, readErr := os.ReadFile(ledgerPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if readErr == nil {
		var old UTCProjection
		decoder := json.NewDecoder(bytes.NewReader(oldBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&old); err != nil {
			return fmt.Errorf("UTC projection history corrupt: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("UTC projection history has trailing data")
		}
		oldText, err := old.canonical()
		if err != nil {
			return err
		}
		if old.Scope != next.Scope || old.Instance != next.Instance || old.LocalID != next.LocalID {
			return fmt.Errorf("UTC projection scope mismatch")
		}
		if next.ConfigGen < old.ConfigGen || next.ConfigGen == old.ConfigGen && !bytes.Equal(text, oldText) {
			return fmt.Errorf("UTC configuration rewind/conflict")
		}
		byPeer := map[uint64]UTCPeerPolicy{}
		for _, peer := range next.Peers {
			byPeer[peer.PeerID] = peer
		}
		for _, peer := range old.Peers {
			current, ok := byPeer[peer.PeerID]
			if !ok {
				return fmt.Errorf("UTC projection must retain historical peer %d", peer.PeerID)
			}
			if current.PairID != peer.PairID || current.PairGen < peer.PairGen || current.PairGen == peer.PairGen && current != peer {
				return fmt.Errorf("UTC pair rewind/conflict")
			}
		}
	} else {
		managed, err := d.utcManaged()
		if err != nil {
			return err
		}
		if managed {
			return fmt.Errorf("UTC projection history missing: recovery required")
		}
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0700); err != nil {
		return err
	}
	if err := durableUTCWrite(ledgerPath, encoded); err != nil {
		return err
	}
	if err := d.MarkUTCManaged(); err != nil {
		return err
	}
	return durableUTCWrite(filepath.Join(d.ConfDir, "utc.conf"), text)
}
