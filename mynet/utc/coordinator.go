// Package utc coordinates a pair through existing authenticated executors.
// It owns no sockets, VPN packets, private keys or platform authorization rules.
package utc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"time"
)

type Side struct {
	NodeID   uint64 `json:"node_id"`
	Instance string `json:"instance"`
	Export   string `json:"export"`
	ScopeID  uint32 `json:"scope_id"`
}
type Plan struct {
	OperationID string   `json:"operation_id"`
	Scope       string   `json:"scope"`
	PairID      uint64   `json:"pair_id"`
	Generation  uint64   `json:"generation"`
	Manifest    string   `json:"manifest"`
	ValidUntil  uint64   `json:"valid_until"`
	LeaseRefs   []string `json:"lease_refs"`
	Sides       [2]Side  `json:"sides"`
}
type Receipt struct {
	OperationID string `json:"operation_id"`
	Scope       string `json:"scope"`
	PairID      uint64 `json:"pair_id"`
	Generation  uint64 `json:"generation"`
	Manifest    string `json:"manifest"`
	NodeID      uint64 `json:"node_id"`
	BootID      string `json:"boot_id"`
	PeerBootID  string `json:"peer_boot_id"`
	PID         uint64 `json:"pid"`
	ObservedAt  uint64 `json:"observed_at"`
	ValidUntil  uint64 `json:"valid_until"`
	State       string `json:"state"`
	Sent        uint64 `json:"sent"`
	Received    uint64 `json:"received"`
}
type Record struct {
	Plan           Plan       `json:"plan"`
	Phase          string     `json:"phase"`
	Watermark      uint64     `json:"watermark"`
	Prepared       [2]Receipt `json:"prepared"`
	Applied        [2]Receipt `json:"applied"`
	Verified       [2]Receipt `json:"verified"`
	BlockPlan      *Plan      `json:"block_plan,omitempty"`
	Blocked        [2]Receipt `json:"blocked"`
	EffectiveUntil uint64     `json:"effective_until"`
	Reason         string     `json:"reason"`
}

// Authority is the trusted authorization adapter. Implementations must validate
// current signatures/revocations and the exact pair generation, manifest,
// endpoints and subjects; a successful TLS connection alone is insufficient.
type Authority interface {
	Validate(context.Context, Plan) error
}

// Node implementations belong to the executor adapter. Apply must read actual
// GNB APPLIED; Verify must measure operation-bound TUN traffic and counters.
// Prepare is a read-only check of bindings already owned by the binding service.
// Returning a configuration-write acknowledgement here violates the contract.
type Node interface {
	Prepare(context.Context, Plan, int) (Receipt, error)
	Apply(context.Context, Plan, int, Receipt) (Receipt, error)
	Verify(context.Context, Plan, int, [2]Receipt) (Receipt, error)
	Block(context.Context, Plan, int) (Receipt, error)
}
type Store interface {
	Lock(context.Context, string, uint64) (func(), error)
	Load(string, uint64) (*Record, error)
	Save(Record) error
}
type Coordinator struct {
	Store     Store
	Authority Authority
	Nodes     map[uint64]Node
	Now       func() time.Time
}

func token(s string) bool {
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
func (p Plan) Digest() string {
	p.Manifest = ""
	p.OperationID = ""
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (p Plan) validate() error {
	if !token(p.OperationID) || !token(p.Scope) || p.PairID == 0 || p.Generation == 0 || p.ValidUntil == 0 || len(p.LeaseRefs) == 0 || len(p.LeaseRefs) > 8 || p.Sides[0].NodeID == 0 || p.Sides[1].NodeID == 0 || p.Sides[0].NodeID == p.Sides[1].NodeID || p.Manifest != p.Digest() {
		return errors.New("invalid UTC pair plan")
	}
	for _, side := range p.Sides {
		endpoint, err := netip.ParseAddrPort(side.Export)
		if !token(side.Instance) || err != nil || endpoint.Port() == 0 || endpoint.Addr().Zone() != "" || endpoint.Addr().Is4In6() || endpoint.Addr().Is4() && side.ScopeID != 0 {
			return errors.New("invalid UTC side")
		}
	}
	seen := map[string]bool{}
	for _, ref := range p.LeaseRefs {
		if !token(ref) || seen[ref] {
			return errors.New("invalid UTC lease reference")
		}
		seen[ref] = true
	}
	return nil
}
func (c *Coordinator) now() uint64 {
	t := time.Now()
	if c.Now != nil {
		t = c.Now()
	}
	if t.Unix() < 0 {
		return 0
	}
	return uint64(t.Unix())
}
func (c *Coordinator) authorized(ctx context.Context, p Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.now() == 0 {
		return errors.New("UTC trusted clock unavailable")
	}
	if c.now() >= p.ValidUntil {
		return errors.New("UTC authorization expired")
	}
	if err := c.Authority.Validate(ctx, p); err != nil {
		return err
	}
	if c.now() == 0 || c.now() >= p.ValidUntil {
		return errors.New("UTC authorization expired during validation")
	}
	return ctx.Err()
}
func (c *Coordinator) receipt(p Plan, side int, r Receipt, state string) error {
	now := c.now()
	if state != "BLOCKED" && (now == 0 || now >= p.ValidUntil) {
		return errors.New("UTC executor evidence authorization expired")
	}
	boot, err := hex.DecodeString(r.BootID)
	if err != nil || len(boot) != 16 || hex.EncodeToString(boot) != r.BootID || r.PID == 0 || r.OperationID != p.OperationID || r.Scope != p.Scope || r.PairID != p.PairID || r.Generation != p.Generation || r.Manifest != p.Manifest || r.NodeID != p.Sides[side].NodeID || r.State != state || r.ObservedAt > now || now-r.ObservedAt > 5 || r.ValidUntil != p.ValidUntil {
		return errors.New("UTC stale or mismatched executor evidence")
	}
	return nil
}

// Run persists intent before any node mutation. Every retry reauthorizes and
// obtains fresh process evidence. The pair lock outlives all side effects.
func (c *Coordinator) Run(ctx context.Context, p Plan) (Record, error) {
	var record Record
	if c.Store == nil || c.Authority == nil || len(c.Nodes) == 0 {
		return record, errors.New("UTC coordinator adapters required")
	}
	if err := p.validate(); err != nil {
		return record, err
	}
	if p.Generation == math.MaxUint64 {
		return record, errors.New("UTC generation must reserve compensation headroom")
	}
	for _, side := range p.Sides {
		if c.Nodes[side.NodeID] == nil {
			return record, errors.New("UTC executor missing")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock, err := c.Store.Lock(ctx, p.Scope, p.PairID)
	if err != nil {
		return record, err
	}
	defer unlock()
	old, err := c.Store.Load(p.Scope, p.PairID)
	if err != nil {
		return record, err
	}
	if old != nil {
		if old.Plan.OperationID == p.OperationID {
			before, _ := json.Marshal(old.Plan)
			after, _ := json.Marshal(p)
			if string(before) != string(after) {
				return *old, errors.New("UTC operation ID conflict")
			}
			record = *old
			if record.BlockPlan != nil {
				return c.compensate(ctx, record)
			}
			if record.Phase == "APPLYING" || record.Phase == "VERIFYING" {
				return c.beginCompensation(ctx, record, "INTERRUPTED_APPLY")
			}
			if record.Phase == "EFFECTIVE" {
				if err := c.authorized(ctx, p); err != nil {
					return c.beginCompensation(ctx, record, "AUTHORIZATION_REVOKED")
				}
			}
		} else if p.Generation <= old.Watermark {
			return *old, errors.New("UTC pair generation rewind")
		}
	}
	record = Record{Plan: p, Phase: "INTENT", Watermark: p.Generation}
	if err := c.Store.Save(record); err != nil {
		return record, err
	}
	if err := c.authorized(ctx, p); err != nil {
		return c.fail(record, "AUTHORIZATION_REJECTED")
	}
	for i, side := range p.Sides {
		receipt, err := c.Nodes[side.NodeID].Prepare(ctx, p, i)
		if err == nil {
			err = c.receipt(p, i, receipt, "PREPARED")
		}
		if err != nil {
			return c.fail(record, fmt.Sprintf("PREPARE_FAILED_%d", side.NodeID))
		}
		record.Prepared[i] = receipt
	}
	record.Phase = "PREPARED"
	if err := c.Store.Save(record); err != nil {
		return record, err
	}
	// Preparation is read-only. A cancelled/revoked prepare must not mutate
	// the previous path merely because its acknowledgements arrived late.
	if err := c.authorized(ctx, p); err != nil {
		return c.fail(record, "PREPARE_AUTHORIZATION_EXPIRED")
	}
	record.Phase = "APPLYING"
	if err := c.Store.Save(record); err != nil {
		return record, err
	}
	for i, side := range p.Sides {
		if err := c.authorized(ctx, p); err != nil {
			return c.beginCompensation(ctx, record, "AUTHORIZATION_REVOKED")
		}
		receipt, err := c.Nodes[side.NodeID].Apply(ctx, p, i, record.Prepared[i])
		if err == nil {
			err = c.receipt(p, i, receipt, "APPLIED")
		}
		if err == nil && (receipt.BootID != record.Prepared[i].BootID || receipt.PID != record.Prepared[i].PID) {
			err = errors.New("UTC process changed")
		}
		if err != nil {
			return c.beginCompensation(ctx, record, fmt.Sprintf("APPLY_FAILED_%d", side.NodeID))
		}
		record.Applied[i] = receipt
		if err := c.Store.Save(record); err != nil {
			return record, err
		}
	}
	record.Phase = "VERIFYING"
	if err := c.Store.Save(record); err != nil {
		return record, err
	}
	for i, side := range p.Sides {
		if err := c.authorized(ctx, p); err != nil {
			return c.beginCompensation(ctx, record, "AUTHORIZATION_REVOKED")
		}
		receipt, err := c.Nodes[side.NodeID].Verify(ctx, p, i, record.Applied)
		if err == nil {
			err = c.receipt(p, i, receipt, "VERIFIED")
		}
		if err == nil && (receipt.BootID != record.Applied[i].BootID || receipt.PID != record.Applied[i].PID || receipt.PeerBootID != record.Applied[1-i].BootID || receipt.Sent == 0 || receipt.Received == 0) {
			err = errors.New("UTC path verification mismatch")
		}
		if err != nil {
			return c.beginCompensation(ctx, record, fmt.Sprintf("VERIFY_FAILED_%d", side.NodeID))
		}
		record.Verified[i] = receipt
	}
	// Earlier receipts may have become stale while the other executor ran.
	if err := c.authorized(ctx, p); err != nil {
		return c.beginCompensation(ctx, record, "AUTHORIZATION_REVOKED")
	}
	for i := range p.Sides {
		if c.receipt(p, i, record.Applied[i], "APPLIED") != nil || c.receipt(p, i, record.Verified[i], "VERIFIED") != nil {
			return c.beginCompensation(ctx, record, "EVIDENCE_EXPIRED")
		}
	}
	record.Phase = "EFFECTIVE"
	record.EffectiveUntil = p.ValidUntil
	for _, evidence := range append(record.Applied[:], record.Verified[:]...) {
		if evidence.ObservedAt+5 < record.EffectiveUntil {
			record.EffectiveUntil = evidence.ObservedAt + 5
		}
	}
	if err := c.Store.Save(record); err != nil {
		record.Phase = "UNCONFIRMED"
		record.EffectiveUntil = 0
		return record, err
	}
	// Disk confirmation can itself cross the operation deadline or evidence
	// window. Do not return a success that became invalid during fsync.
	if err := c.authorized(ctx, p); err != nil {
		return c.beginCompensation(ctx, record, "CONFIRMATION_INVALIDATED")
	}
	for i := range p.Sides {
		if c.receipt(p, i, record.Applied[i], "APPLIED") != nil || c.receipt(p, i, record.Verified[i], "VERIFIED") != nil {
			return c.beginCompensation(ctx, record, "CONFIRMATION_EVIDENCE_EXPIRED")
		}
	}
	return record, nil
}
func (c *Coordinator) fail(record Record, reason string) (Record, error) {
	record.Phase = "FAILED"
	record.EffectiveUntil = 0
	record.Reason = reason
	if err := c.Store.Save(record); err != nil {
		return record, err
	}
	return record, errors.New(reason)
}
func (c *Coordinator) beginCompensation(ctx context.Context, record Record, reason string) (Record, error) {
	if record.Watermark == math.MaxUint64 {
		return c.fail(record, "GENERATION_EXHAUSTED")
	}
	blocked := record.Plan
	blocked.Generation = record.Watermark + 1
	// Use a fixed past expiry rather than the failure timestamp: a clock
	// correction must not turn a blocking projection into a future lease.
	blocked.ValidUntil = 1
	if c.now() == 0 {
		return c.fail(record, "CLOCK_INVALID")
	}
	blocked.Manifest = blocked.Digest()
	record.BlockPlan = &blocked
	record.Watermark = blocked.Generation
	record.Phase = "COMPENSATING"
	record.EffectiveUntil = 0
	record.Reason = reason
	if err := c.Store.Save(record); err != nil {
		return record, err
	}
	return c.compensate(ctx, record)
}
func (c *Coordinator) compensate(ctx context.Context, record Record) (Record, error) {
	p := *record.BlockPlan
	complete := true
	// A timed-out apply context cannot also be the entire recovery budget. Late
	// writes carry the older generation and will be fenced by node high water.
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for i, side := range p.Sides {
		receipt, err := c.Nodes[side.NodeID].Block(recovery, p, i)
		if err == nil {
			err = c.receipt(p, i, receipt, "BLOCKED")
		}
		if err != nil {
			complete = false
			continue
		}
		record.Blocked[i] = receipt
	}
	record.Phase = "BLOCK_EXPECTED"
	if complete {
		record.Phase = "BLOCKED"
	}
	if err := c.Store.Save(record); err != nil {
		return record, err
	}
	return record, errors.New(record.Reason)
}

// Status reports bounded observations, never a permanent success boolean.
// A revoked/expired authorization or stale confirmation yields DEGRADED.
func (c *Coordinator) Status(ctx context.Context, scope string, pair uint64) (*Record, error) {
	if c.Store == nil || c.Authority == nil {
		return nil, errors.New("UTC coordinator adapters required")
	}
	unlock, err := c.Store.Lock(ctx, scope, pair)
	if err != nil {
		return nil, err
	}
	defer unlock()
	record, err := c.Store.Load(scope, pair)
	if err != nil || record == nil {
		return record, err
	}
	if record.Phase == "EFFECTIVE" {
		fresh := c.now() < record.EffectiveUntil && c.authorized(ctx, record.Plan) == nil
		for i := range record.Plan.Sides {
			fresh = fresh && c.receipt(record.Plan, i, record.Applied[i], "APPLIED") == nil && c.receipt(record.Plan, i, record.Verified[i], "VERIFIED") == nil
		}
		if !fresh {
			record.Phase = "DEGRADED"
			record.EffectiveUntil = 0
		}
	}
	return record, nil
}
