package hubd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/mynet-club/mynet_agent/mynet/utc/hub"
)

const maxProjectionBytes = 131072

// Verified is the durable "effectively applied" watermark. It is written only
// after utc.status reports the matching pair APPLIED; the control summary may
// be ahead of it, but a projection is never considered effective without it.
type Verified struct {
	Scope            string `json:"scope"`
	Instance         string `json:"instance"`
	ConfigGen        uint64 `json:"config_gen"`
	PeerID           uint64 `json:"peer_id"`
	PairID           uint64 `json:"pair_id"`
	Side             uint8  `json:"side"`
	Mode             uint8  `json:"mode"`
	PairGen          uint64 `json:"pair_gen"`
	LeaseEpoch       uint64 `json:"lease_epoch"`
	PlacementEpoch   uint64 `json:"placement_epoch"`
	EdgeID           uint32 `json:"edge_id"`
	ShardID          uint32 `json:"shard_id"`
	ReactorID        uint32 `json:"reactor_id"`
	ValidUntil       uint64 `json:"valid_until"`
	ObservedAt       uint64 `json:"observed_at"`
	ProjectionDigest string `json:"projection_sha256"`
}

// Prior rebuilds the fence/epoch state of the last applied projection.
func (v Verified) Prior() hub.Prior {
	return hub.Prior{
		PairGen:    v.PairGen,
		LeaseEpoch: v.LeaseEpoch,
		Placement: hub.Placement{
			Epoch: v.PlacementEpoch, EdgeID: v.EdgeID,
			ShardID: v.ShardID, ReactorID: v.ReactorID,
		},
	}
}

func loadVerified(path string) (Verified, error) {
	var verified Verified
	info, err := os.Lstat(path)
	if err != nil {
		return verified, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 65536 {
		return verified, errors.New("utc-hub verified watermark must be a bounded private file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return verified, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&verified); err != nil {
		return Verified{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Verified{}, errors.New("utc-hub verified watermark has trailing data")
	}
	if verified.Scope == "" || verified.PeerID == 0 || verified.PairID == 0 ||
		verified.ConfigGen == 0 || verified.PairGen == 0 || verified.LeaseEpoch == 0 ||
		verified.PlacementEpoch == 0 || verified.EdgeID == 0 || verified.ValidUntil == 0 ||
		verified.ObservedAt == 0 || len(verified.ProjectionDigest) != 64 {
		return Verified{}, errors.New("utc-hub verified watermark is incomplete")
	}
	if _, err := hex.DecodeString(verified.ProjectionDigest); err != nil {
		return Verified{}, errors.New("utc-hub verified watermark digest is invalid")
	}
	return verified, nil
}

type projectionState struct {
	Exists    bool
	Digest    string
	ConfigGen uint64
	PairGen   uint64
	ExpiresAt uint64
}

func inspectProjection(path string, identity hub.Identity, pairID uint64) (projectionState, error) {
	var state projectionState
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxProjectionBytes {
		return state, errors.New("utc.conf must be a bounded private file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	projection, err := hub.Parse(raw)
	if err != nil {
		return state, err
	}
	if projection.Scope != identity.Scope || projection.Instance != identity.Instance ||
		projection.LocalID != identity.LocalID {
		return state, errors.New("utc.conf belongs to another scope/instance")
	}
	if len(projection.Peers) != 1 {
		return state, errors.New("utc.conf is not owned by this single-pair runtime")
	}
	peer := projection.Peers[0]
	if peer.PeerID != identity.PeerID || peer.PairID != pairID {
		return state, errors.New("utc.conf does not describe the configured pair")
	}
	sum := sha256.Sum256(raw)
	return projectionState{
		Exists: true, Digest: hex.EncodeToString(sum[:]),
		ConfigGen: projection.ConfigGen, PairGen: peer.PairGen,
		ExpiresAt: peer.ExpiresAt,
	}, nil
}

func projectionDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

var errProjectionMismatch = errors.New("utc.conf does not match the verified watermark")
var errState = errors.New("utc hub state requires operator repair")

func permanent(err error) error {
	if err == nil {
		return nil
	}
	return &stateError{err: err}
}

type stateError struct{ err error }

func (e *stateError) Error() string        { return e.err.Error() }
func (e *stateError) Unwrap() error        { return e.err }
func (e *stateError) Is(target error) bool { return target == errState }
