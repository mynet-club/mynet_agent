package hubd

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/utc/hub"
)

const maxStatusBytes = 131072

type statusHeader struct {
	Scope       string
	Instance    string
	LocalID     uint64
	BootID      string
	ConfigGen   uint64
	GeneratedAt uint64
	Reason      string
	PID         uint64
}

type statusPeer struct {
	PeerID      uint64
	PairID      uint64
	PairGen     uint64
	State       string
	Reason      string
	Manifest    string
	LocalDigest string
}

type statusUsage struct {
	Tx uint64
	Rx uint64
}

type statusSnapshot struct {
	Header statusHeader
	Peers  map[uint64]statusPeer
	Usage  map[uint64]statusUsage
}

// readStatusFile enforces the GNB-written status file boundary: regular,
// private, bounded and newline terminated. A partial or replaced file never
// counts as evidence.
func readStatusFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxStatusBytes {
		return nil, errors.New("utc.status must be a bounded private file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return nil, errors.New("utc.status is incomplete")
	}
	return raw, nil
}

func parseStatus(raw []byte) (statusSnapshot, error) {
	var snapshot statusSnapshot
	text := string(raw)
	if !strings.HasSuffix(text, "\n") {
		return snapshot, errors.New("utc.status is incomplete")
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 2 {
		return snapshot, errors.New("utc.status is too short")
	}
	header := strings.Fields(lines[0])
	if len(header) != 9 || header[0] != "status-v1" {
		return snapshot, errors.New("utc.status header is invalid")
	}
	var err error
	if snapshot.Header.LocalID, err = strconv.ParseUint(header[3], 10, 64); err != nil {
		return snapshot, errors.New("utc.status local id is invalid")
	}
	if snapshot.Header.ConfigGen, err = strconv.ParseUint(header[5], 10, 64); err != nil {
		return snapshot, errors.New("utc.status config gen is invalid")
	}
	if snapshot.Header.GeneratedAt, err = strconv.ParseUint(header[6], 10, 64); err != nil {
		return snapshot, errors.New("utc.status timestamp is invalid")
	}
	if snapshot.Header.PID, err = strconv.ParseUint(header[8], 10, 64); err != nil {
		return snapshot, errors.New("utc.status pid is invalid")
	}
	snapshot.Header.Scope, snapshot.Header.Instance = header[1], header[2]
	snapshot.Header.BootID, snapshot.Header.Reason = header[4], header[7]
	if len(snapshot.Header.BootID) != 32 {
		return snapshot, errors.New("utc.status boot id is invalid")
	}
	if _, err := hex.DecodeString(snapshot.Header.BootID); err != nil {
		return snapshot, errors.New("utc.status boot id is invalid")
	}
	metrics := strings.Fields(lines[1])
	if len(metrics) != 5 || metrics[0] != "metrics" {
		return snapshot, errors.New("utc.status metrics row is invalid")
	}
	for _, field := range metrics[1:] {
		if _, err := strconv.ParseUint(field, 10, 64); err != nil {
			return snapshot, errors.New("utc.status metric is invalid")
		}
	}
	snapshot.Peers = make(map[uint64]statusPeer)
	snapshot.Usage = make(map[uint64]statusUsage)
	for _, line := range lines[2:] {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
			return snapshot, errors.New("utc.status has an empty row")
		case fields[0] == "policy" && len(fields) >= 2:
			continue
		case fields[0] == "peer" && len(fields) == 8:
			peer, err := parseStatusPeer(fields)
			if err != nil {
				return snapshot, err
			}
			if _, duplicate := snapshot.Peers[peer.PeerID]; duplicate {
				return snapshot, errors.New("utc.status repeats a peer")
			}
			snapshot.Peers[peer.PeerID] = peer
		case fields[0] == "usage" && len(fields) == 4:
			peerID, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || peerID == 0 {
				return snapshot, errors.New("utc.status usage peer is invalid")
			}
			tx, err := strconv.ParseUint(fields[2], 10, 64)
			if err != nil {
				return snapshot, errors.New("utc.status usage tx is invalid")
			}
			rx, err := strconv.ParseUint(fields[3], 10, 64)
			if err != nil {
				return snapshot, errors.New("utc.status usage rx is invalid")
			}
			if _, duplicate := snapshot.Usage[peerID]; duplicate {
				return snapshot, errors.New("utc.status repeats usage")
			}
			snapshot.Usage[peerID] = statusUsage{Tx: tx, Rx: rx}
		default:
			return snapshot, errors.New("utc.status has an unknown row")
		}
	}
	return snapshot, nil
}

func parseStatusPeer(fields []string) (statusPeer, error) {
	var peer statusPeer
	var err error
	if peer.PeerID, err = strconv.ParseUint(fields[1], 10, 64); err != nil {
		return peer, errors.New("utc.status peer id is invalid")
	}
	if peer.PairID, err = strconv.ParseUint(fields[2], 10, 64); err != nil {
		return peer, errors.New("utc.status pair id is invalid")
	}
	if peer.PairGen, err = strconv.ParseUint(fields[3], 10, 64); err != nil {
		return peer, errors.New("utc.status pair gen is invalid")
	}
	peer.State, peer.Reason = fields[4], fields[5]
	if peer.State != "APPLIED" && peer.State != "BLOCKED" {
		return peer, errors.New("utc.status peer state is invalid")
	}
	peer.Manifest, peer.LocalDigest = fields[6], fields[7]
	manifest, err := hex.DecodeString(peer.Manifest)
	if err != nil || len(manifest) != 32 || hex.EncodeToString(manifest) != peer.Manifest {
		return peer, errors.New("utc.status peer manifest is invalid")
	}
	local, err := hex.DecodeString(peer.LocalDigest)
	if err != nil || len(local) != 64 || hex.EncodeToString(local) != peer.LocalDigest {
		return peer, errors.New("utc.status peer digest is invalid")
	}
	return peer, nil
}

// applied checks the only success evidence the runtime accepts: a fresh
// status header bound to this process and a matching pair row in APPLIED.
func (s statusSnapshot) applied(identity hub.Identity, pairID, pairGen uint64,
	now time.Time, maxAge time.Duration) error {
	if s.Header.Scope != identity.Scope || s.Header.Instance != identity.Instance ||
		s.Header.LocalID != identity.LocalID || s.Header.BootID != identity.BootID ||
		s.Header.ConfigGen != identity.ConfigGen || s.Header.PID != identity.PID {
		return errors.New("utc.status is not bound to the configured process")
	}
	nowUnix := now.Unix()
	if s.Header.GeneratedAt > uint64(nowUnix) || nowUnix-int64(s.Header.GeneratedAt) > int64(maxAge/time.Second) {
		return errors.New("utc.status is stale or future dated")
	}
	peer, ok := s.Peers[identity.PeerID]
	if !ok || peer.PairID != pairID {
		return errors.New("utc.status has no row for the managed pair")
	}
	if peer.State != "APPLIED" {
		return fmt.Errorf("utc.status pair is %s: %s", peer.State, peer.Reason)
	}
	if peer.PairGen != pairGen {
		return fmt.Errorf("utc.status pair_gen %d does not match %d", peer.PairGen, pairGen)
	}
	return nil
}
