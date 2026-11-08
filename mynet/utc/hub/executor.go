package hub

import (
	"context"
	"errors"
	"fmt"
)

// Node is the executor seam for one side of a Hub plan. Apply must report the
// GNB APPLIED state only after the stream is ready; Verify must return
// operation-bound counters for this plan, never instance-cumulative totals.
// The executor never touches any peer outside the plan's two sides.
type Node interface {
	Apply(ctx context.Context, plan Plan, side int) (Receipt, error)
	Verify(ctx context.Context, plan Plan, side int) (Receipt, error)
}

// Executor drives one plan through APPLIED, VERIFIED and EFFECTIVE. It performs
// no I/O itself; nodes own transport and GNB publication.
type Executor struct{}

// Run applies both sides, requires bidirectional operation-bound evidence and
// returns an EFFECTIVE record. On any failure the phase is FAILED and the
// returned record carries every receipt collected so far; the caller decides
// whether to compensate with a newer generation.
func (e Executor) Run(ctx context.Context, plan Plan, nodes [2]Node) (Record, error) {
	record := Record{Plan: plan, Phase: PhaseIntent, Watermark: plan.PairGen}
	if err := plan.Validate(); err != nil {
		record.Phase = PhaseFailed
		record.Reason = err.Error()
		return record, err
	}
	if nodes[0] == nil || nodes[1] == nil {
		err := errors.New("hub executor requires both side nodes")
		record.Phase = PhaseFailed
		record.Reason = err.Error()
		return record, err
	}
	for side := 0; side < 2; side++ {
		receipt, err := nodes[side].Apply(ctx, plan, side)
		if err != nil {
			record.Phase = PhaseFailed
			record.Reason = err.Error()
			return record, fmt.Errorf("apply side %d: %w", side, err)
		}
		record.Applied[side] = receipt
	}
	record.Phase = PhaseApplying
	for side := 0; side < 2; side++ {
		if err := appliedEvidence(plan, side, record.Applied[side]); err != nil {
			record.Phase = PhaseFailed
			record.Reason = err.Error()
			return record, err
		}
	}
	for side := 0; side < 2; side++ {
		receipt, err := nodes[side].Verify(ctx, plan, side)
		if err != nil {
			record.Phase = PhaseFailed
			record.Reason = err.Error()
			return record, fmt.Errorf("verify side %d: %w", side, err)
		}
		record.Verified[side] = receipt
	}
	record.Phase = PhaseVerifying
	effectiveUntil := plan.ValidUntil
	for side := 0; side < 2; side++ {
		if err := verifiedEvidence(plan, side, record.Verified[side]); err != nil {
			record.Phase = PhaseFailed
			record.Reason = err.Error()
			return record, err
		}
		if record.Verified[side].ObservedAt+5 < effectiveUntil {
			effectiveUntil = record.Verified[side].ObservedAt + 5
		}
	}
	if effectiveUntil == 0 {
		err := errors.New("hub executor has no effective window")
		record.Phase = PhaseFailed
		record.Reason = err.Error()
		return record, err
	}
	record.EffectiveUntil = effectiveUntil
	record.Phase = PhaseEffective
	if err := record.Validate(); err != nil {
		record.Phase = PhaseFailed
		record.Reason = err.Error()
		return record, err
	}
	return record, nil
}

// Restore replays the last effective plan as an explicit compensation and
// returns its EFFECTIVE record. The caller must pass a plan generation that is
// acceptable to the driver; this method never fabricates a newer epoch.
func (e Executor) Restore(ctx context.Context, previous Plan, nodes [2]Node) (Record, error) {
	return e.Run(ctx, previous, nodes)
}

func appliedEvidence(plan Plan, side int, receipt Receipt) error {
	if receipt.OperationID == "" {
		return fmt.Errorf("side %d has no APPLIED receipt", side)
	}
	if receipt.State != StateApplied || receipt.NodeID != plan.Sides[side].NodeID {
		return fmt.Errorf("side %d APPLIED receipt is not bound to the plan", side)
	}
	return nil
}

func verifiedEvidence(plan Plan, side int, receipt Receipt) error {
	if receipt.OperationID == "" {
		return fmt.Errorf("side %d has no VERIFIED receipt", side)
	}
	if receipt.State != StateVerified || receipt.NodeID != plan.Sides[side].NodeID {
		return fmt.Errorf("side %d VERIFIED receipt is not bound to the plan", side)
	}
	if receipt.Sent == 0 || receipt.Received == 0 {
		return fmt.Errorf("side %d VERIFIED receipt has no operation-bound counters", side)
	}
	return nil
}
