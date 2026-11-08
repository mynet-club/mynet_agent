package hub

import "testing"

func samplePlan(t *testing.T) Plan {
	t.Helper()
	plan := Plan{
		OperationID:    "op-1",
		Scope:          "scope.test",
		PairID:         30,
		PairGen:        2,
		LeaseEpoch:     7,
		PlacementEpoch: 3,
		ValidUntil:     200,
		Sides: [2]Side{
			{NodeID: 10, Instance: "a", Export: "127.0.0.1:14001"},
			{NodeID: 20, Instance: "b", Export: "127.0.0.1:14002"},
		},
	}
	digested, err := plan.WithDigest()
	if err != nil {
		t.Fatal(err)
	}
	return digested
}

func sampleReceipt(plan Plan, node uint64) Receipt {
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
		ObservedAt:     150,
		ValidUntil:     plan.ValidUntil,
		State:          "VERIFIED",
		ProbeDigest:    "probe",
		Sent:           10,
		Received:       10,
	}
}

func TestPlanAndRecord(t *testing.T) {
	plan := samplePlan(t)
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	record := Record{
		Plan:      plan,
		Phase:     PhaseEffective,
		Watermark: plan.PairGen,
		Applied: [2]Receipt{
			sampleReceipt(plan, 10),
			sampleReceipt(plan, 20),
		},
		Verified: [2]Receipt{
			sampleReceipt(plan, 10),
			sampleReceipt(plan, 20),
		},
		EffectiveUntil: plan.ValidUntil,
	}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	record.Verified[1].OperationID = "other"
	if err := record.Validate(); err == nil {
		t.Fatal("accepted receipt from another operation")
	}
	record = Record{Plan: plan, Phase: PhaseEffective, Watermark: plan.PairGen}
	if err := record.Validate(); err == nil {
		t.Fatal("accepted EFFECTIVE without both sides")
	}
	if err := (Record{Plan: plan, Phase: "BOGUS", Watermark: plan.PairGen}).Validate(); err == nil {
		t.Fatal("accepted unknown phase")
	}
	if err := (Record{Plan: plan, Phase: PhaseIntent, Watermark: 1}).Validate(); err == nil {
		t.Fatal("accepted watermark regression")
	}
}
