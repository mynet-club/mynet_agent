package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
)

const (
	PhaseIntent        = "INTENT"
	PhasePrepared      = "PREPARED"
	PhaseApplying      = "APPLYING"
	PhaseVerifying     = "VERIFYING"
	PhaseEffective     = "EFFECTIVE"
	PhaseUnconfirmed   = "UNCONFIRMED"
	PhaseFailed        = "FAILED"
	PhaseCompensating  = "COMPENSATING"
	PhaseBlocked       = "BLOCKED"
	PhaseBlockExpected = "BLOCK_EXPECTED"
	PhaseDegraded      = "DEGRADED"
	StateApplied       = "APPLIED"
	StateVerified      = "VERIFIED"
)

type Side struct {
	NodeID   uint64
	Instance string
	Export   string
	ScopeID  uint32
}

type Plan struct {
	OperationID    string
	Scope          string
	PairID         uint64
	PairGen        uint64
	Manifest       string
	LeaseEpoch     uint64
	PlacementEpoch uint64
	ValidUntil     uint64
	Sides          [2]Side
}

type Receipt struct {
	OperationID    string
	Scope          string
	PairID         uint64
	PairGen        uint64
	LeaseEpoch     uint64
	PlacementEpoch uint64
	NodeID         uint64
	BootID         string
	PID            uint64
	ObservedAt     uint64
	ValidUntil     uint64
	State          string
	Sent           uint64
	Received       uint64
	ProbeDigest    string
}

type Record struct {
	Plan           Plan
	Phase          string
	Watermark      uint64
	Applied        [2]Receipt
	Verified       [2]Receipt
	EffectiveUntil uint64
	Reason         string
}

// digest covers the actionable plan content: scope, pair identity, epochs,
// validity window and sides. Manifest is excluded as the digest itself.
// OperationID is excluded because it is the coordinator's operation label,
// not pair content, and its encoding is a design-deferred item; binding it
// into the digest would make plans unverifiable when that encoding lands.
func (p Plan) digest() (string, error) {
	copyPlan := p
	copyPlan.Manifest = ""
	copyPlan.OperationID = ""
	raw, err := json.Marshal(copyPlan)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (p Plan) Validate() error {
	if !token(p.OperationID) || !token(p.Scope) || p.PairID == 0 || p.PairGen == 0 ||
		p.LeaseEpoch == 0 || p.PlacementEpoch == 0 || p.ValidUntil == 0 ||
		p.Sides[0].NodeID == 0 || p.Sides[1].NodeID == 0 ||
		p.Sides[0].NodeID == p.Sides[1].NodeID {
		return fmt.Errorf("invalid Hub plan identity")
	}
	for _, side := range p.Sides {
		endpoint, err := netip.ParseAddrPort(side.Export)
		if !token(side.Instance) || err != nil || endpoint.Port() == 0 ||
			endpoint.Addr().Zone() != "" || endpoint.Addr().Is4In6() ||
			endpoint.Addr().Is4() && side.ScopeID != 0 {
			return fmt.Errorf("invalid Hub plan side")
		}
	}
	digest, err := p.digest()
	if err != nil || digest != p.Manifest {
		return fmt.Errorf("invalid Hub plan manifest")
	}
	return nil
}

func (p Plan) WithDigest() (Plan, error) {
	digest, err := p.digest()
	if err != nil {
		return Plan{}, err
	}
	p.Manifest = digest
	return p, nil
}

func (r Record) Validate() error {
	if err := r.Plan.Validate(); err != nil {
		return err
	}
	switch r.Phase {
	case PhaseIntent, PhasePrepared, PhaseApplying, PhaseVerifying, PhaseEffective,
		PhaseUnconfirmed, PhaseFailed, PhaseCompensating, PhaseBlocked,
		PhaseBlockExpected, PhaseDegraded:
	default:
		return fmt.Errorf("unknown Hub phase")
	}
	if r.Watermark < r.Plan.PairGen {
		return fmt.Errorf("Hub watermark regresses below plan")
	}
	if r.EffectiveUntil != 0 && r.EffectiveUntil > r.Plan.ValidUntil {
		return fmt.Errorf("Hub effective window exceeds plan validity")
	}
	for side := 0; side < 2; side++ {
		if err := r.receipt(side, r.Applied[side], StateApplied, false); err != nil {
			return err
		}
		if err := r.receipt(side, r.Verified[side], StateVerified, true); err != nil {
			return err
		}
	}
	if r.Phase == PhaseEffective &&
		(r.Applied[0].OperationID == "" || r.Applied[1].OperationID == "" ||
			r.Verified[0].OperationID == "" || r.Verified[1].OperationID == "" ||
			r.EffectiveUntil == 0) {
		return fmt.Errorf("EFFECTIVE requires both APPLIED and VERIFIED sides")
	}
	return nil
}

func (r Record) receipt(side int, receipt Receipt, state string, counters bool) error {
	if receipt.OperationID == "" {
		return nil
	}
	if receipt.OperationID != r.Plan.OperationID || receipt.Scope != r.Plan.Scope ||
		receipt.PairID != r.Plan.PairID || receipt.PairGen != r.Plan.PairGen ||
		receipt.LeaseEpoch != r.Plan.LeaseEpoch ||
		receipt.PlacementEpoch != r.Plan.PlacementEpoch ||
		receipt.NodeID != r.Plan.Sides[side].NodeID || receipt.State != state ||
		receipt.BootID == "" || receipt.PID == 0 || receipt.ObservedAt == 0 ||
		receipt.ValidUntil != r.Plan.ValidUntil || receipt.ProbeDigest == "" ||
		counters && (receipt.Sent == 0 || receipt.Received == 0) {
		return fmt.Errorf("Hub receipt is not bound to the plan operation")
	}
	return nil
}
