package endpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Pair intent is the user's *desire* to use UTC TCP with one peer. It is
// deliberately separate from execution state: the cloud owns authorization and
// the gnb utc-v2 projection owns what is actually applied. A local intent only
// asks the cloud to create/keep a pair; the node never flips mode=2 on its own
// from this file.
const (
	PairIntentOn  = "on"
	PairIntentOff = "off"

	// LastResult values. "pending" means the intent has not been confirmed by
	// the cloud yet (or the cloud path is not wired); "active" means the pair
	// exists in the projection; "rejected" carries a user-readable reason.
	PairResultPending  = "pending"
	PairResultActive   = "active"
	PairResultRejected = "rejected"

	pairIntentVersion = 1
	pairIntentMax     = 256
)

// PairIntent is one peer's requested TCP state plus the last cloud outcome.
type PairIntent struct {
	PeerID      uint64 `json:"peer_id"`
	Intent      string `json:"intent"`
	RequestedAt int64  `json:"requested_at,omitempty"`
	LastSyncAt  int64  `json:"last_sync_at,omitempty"`
	LastResult  string `json:"last_result,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// Enabled reports whether the user asked for TCP with this peer.
func (p PairIntent) Enabled() bool { return p.Intent == PairIntentOn }

// PairIntentPath is the per-node intent file. It lives next to utc_state.json
// under $MYNET_HOME/conf/local (the 2-layer "intent" area), never under the
// per-form node conf dir that the bundle projection owns.
func PairIntentPath(myNetHome string) string {
	return filepath.Join(myNetHome, "conf", "local", "utc_pairs.json")
}

type pairIntentFile struct {
	Version int          `json:"version"`
	Pairs   []PairIntent `json:"pairs"`
}

// PairIntentStore is a small, atomically-persisted map keyed by peer id.
type PairIntentStore struct {
	mu    sync.Mutex
	path  string
	pairs map[uint64]PairIntent
}

// LoadPairIntents reads the intent file. A missing file is an empty store.
func LoadPairIntents(myNetHome string) (*PairIntentStore, error) {
	store := &PairIntentStore{
		path:  PairIntentPath(myNetHome),
		pairs: make(map[uint64]PairIntent),
	}
	raw, err := os.ReadFile(store.path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pair intents: %w", err)
	}
	var file pairIntentFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse pair intents: %w", err)
	}
	if file.Version != 0 && file.Version != pairIntentVersion {
		return nil, fmt.Errorf("unsupported pair intent version %d", file.Version)
	}
	if len(file.Pairs) > pairIntentMax {
		return nil, fmt.Errorf("pair intents exceed %d entries", pairIntentMax)
	}
	for _, pair := range file.Pairs {
		if pair.PeerID == 0 {
			return nil, errors.New("pair intent has peer_id 0")
		}
		if pair.Intent != PairIntentOn && pair.Intent != PairIntentOff {
			return nil, fmt.Errorf("pair intent for peer %d has invalid intent %q", pair.PeerID, pair.Intent)
		}
		store.pairs[pair.PeerID] = pair
	}
	return store, nil
}

// List returns intents sorted by peer id.
func (s *PairIntentStore) List() []PairIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *PairIntentStore) listLocked() []PairIntent {
	out := make([]PairIntent, 0, len(s.pairs))
	for _, pair := range s.pairs {
		out = append(out, pair)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PeerID < out[j].PeerID })
	return out
}

// Get returns one peer's intent.
func (s *PairIntentStore) Get(peerID uint64) (PairIntent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pair, ok := s.pairs[peerID]
	return pair, ok
}

// Set records the user's desired state and persists it before returning so a
// crash never loses an explicit "connect" or "disconnect" request.
func (s *PairIntentStore) Set(peerID uint64, on bool, now int64) (PairIntent, error) {
	if peerID == 0 {
		return PairIntent{}, errors.New("peer_id must not be 0")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pair := s.pairs[peerID]
	pair.PeerID = peerID
	if on {
		pair.Intent = PairIntentOn
		if pair.LastResult != PairResultActive {
			pair.LastResult = PairResultPending
		}
	} else {
		pair.Intent = PairIntentOff
		pair.LastResult = ""
		pair.Reason = ""
	}
	pair.RequestedAt = now
	s.pairs[peerID] = pair
	if len(s.pairs) > pairIntentMax {
		return PairIntent{}, fmt.Errorf("pair intents exceed %d entries", pairIntentMax)
	}
	if err := s.saveLocked(); err != nil {
		return PairIntent{}, err
	}
	return pair, nil
}

// SetResult records the last cloud outcome for an existing intent.
func (s *PairIntentStore) SetResult(peerID uint64, result, reason string, now int64) (PairIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pair, ok := s.pairs[peerID]
	if !ok {
		return PairIntent{}, fmt.Errorf("no intent for peer %d", peerID)
	}
	pair.LastResult = result
	pair.Reason = reason
	pair.LastSyncAt = now
	s.pairs[peerID] = pair
	if err := s.saveLocked(); err != nil {
		return PairIntent{}, err
	}
	return pair, nil
}

func (s *PairIntentStore) saveLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create intent dir: %w", err)
	}
	file := pairIntentFile{Version: pairIntentVersion, Pairs: s.listLocked()}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(dir, ".utc-pairs-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	if dirfd, err := os.Open(dir); err == nil {
		_ = dirfd.Sync()
		_ = dirfd.Close()
	}
	return nil
}
