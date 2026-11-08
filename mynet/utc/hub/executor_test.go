package hub

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeNode struct {
	nodeID       uint64
	observedAt   uint64
	applyCalls   int
	verifyCalls  int
	failApply    bool
	failVerify   bool
	zeroCounters bool
	wrongNode    bool
	lastPlan     Plan
	lastSide     int
}

func (f *fakeNode) receipt(plan Plan, state string) Receipt {
	node := f.nodeID
	if f.wrongNode {
		node = 999
	}
	sent, received := uint64(10), uint64(10)
	if f.zeroCounters && state == StateVerified {
		sent, received = 0, 0
	}
	return Receipt{
		OperationID:    plan.OperationID,
		Scope:          plan.Scope,
		PairID:         plan.PairID,
		PairGen:        plan.PairGen,
		LeaseEpoch:     plan.LeaseEpoch,
		PlacementEpoch: plan.PlacementEpoch,
		NodeID:         node,
		BootID:         "boot",
		PID:            100,
		ObservedAt:     f.observedAt,
		ValidUntil:     plan.ValidUntil,
		State:          state,
		Sent:           sent,
		Received:       received,
		ProbeDigest:    "probe",
	}
}

func (f *fakeNode) Apply(_ context.Context, plan Plan, side int) (Receipt, error) {
	f.applyCalls++
	f.lastPlan = plan
	f.lastSide = side
	if f.failApply {
		return Receipt{}, errors.New("apply failed")
	}
	return f.receipt(plan, StateApplied), nil
}

func (f *fakeNode) Verify(_ context.Context, plan Plan, side int) (Receipt, error) {
	f.verifyCalls++
	f.lastPlan = plan
	f.lastSide = side
	if f.failVerify {
		return Receipt{}, errors.New("verify failed")
	}
	return f.receipt(plan, StateVerified), nil
}

func TestExecutorRunEffective(t *testing.T) {
	plan := samplePlan(t)
	nodes := [2]Node{
		&fakeNode{nodeID: 10, observedAt: 100},
		&fakeNode{nodeID: 20, observedAt: 100},
	}
	record, err := (Executor{}).Run(context.Background(), plan, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if record.Phase != PhaseEffective {
		t.Fatalf("phase = %s", record.Phase)
	}
	if record.EffectiveUntil != 105 {
		t.Fatalf("effective until = %d", record.EffectiveUntil)
	}
	for side, node := range nodes {
		fake := node.(*fakeNode)
		if fake.applyCalls != 1 || fake.verifyCalls != 1 || fake.lastSide != side {
			t.Fatalf("side %d calls = %d/%d", side, fake.applyCalls, fake.verifyCalls)
		}
	}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorFailures(t *testing.T) {
	plan := samplePlan(t)
	cases := []struct {
		name  string
		first *fakeNode
		last  *fakeNode
	}{
		{"apply fails on side B", &fakeNode{nodeID: 10, observedAt: 100}, &fakeNode{nodeID: 20, observedAt: 100, failApply: true}},
		{"verify fails on side A", &fakeNode{nodeID: 10, observedAt: 100, failVerify: true}, &fakeNode{nodeID: 20, observedAt: 100}},
		{"verified counters are zero", &fakeNode{nodeID: 10, observedAt: 100}, &fakeNode{nodeID: 20, observedAt: 100, zeroCounters: true}},
		{"receipt names another node", &fakeNode{nodeID: 10, observedAt: 100, wrongNode: true}, &fakeNode{nodeID: 20, observedAt: 100}},
	}
	for _, testCase := range cases {
		record, err := (Executor{}).Run(context.Background(), plan, [2]Node{testCase.first, testCase.last})
		if err == nil {
			t.Fatalf("%s: expected an error", testCase.name)
		}
		if record.Phase != PhaseFailed {
			t.Fatalf("%s: phase = %s", testCase.name, record.Phase)
		}
	}
	record, err := (Executor{}).Run(context.Background(), plan, [2]Node{nil, &fakeNode{nodeID: 20, observedAt: 100}})
	if err == nil || record.Phase != PhaseFailed {
		t.Fatal("missing node must fail closed")
	}
	broken := plan
	broken.Manifest = ""
	record, err = (Executor{}).Run(context.Background(), broken, [2]Node{
		&fakeNode{nodeID: 10, observedAt: 100},
		&fakeNode{nodeID: 20, observedAt: 100},
	})
	if err == nil || !strings.Contains(err.Error(), "manifest") || record.Phase != PhaseFailed {
		t.Fatal("invalid plan must fail before any apply")
	}
}

func TestExecutorRestore(t *testing.T) {
	previous := samplePlan(t)
	failing := [2]Node{
		&fakeNode{nodeID: 10, observedAt: 100},
		&fakeNode{nodeID: 20, observedAt: 100, failApply: true},
	}
	if _, err := (Executor{}).Run(context.Background(), previous, failing); err == nil {
		t.Fatal("expected the new plan to fail")
	}
	nodes := [2]Node{
		&fakeNode{nodeID: 10, observedAt: 100},
		&fakeNode{nodeID: 20, observedAt: 100},
	}
	record, err := (Executor{}).Restore(context.Background(), previous, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if record.Phase != PhaseEffective || record.Plan.OperationID != previous.OperationID {
		t.Fatalf("restore record = %+v", record)
	}
	for _, node := range nodes {
		if node.(*fakeNode).lastPlan.OperationID != previous.OperationID {
			t.Fatal("restore must replay the previous plan on both sides")
		}
	}
}
