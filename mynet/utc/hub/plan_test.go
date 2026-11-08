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

func sampleReceipt(plan Plan, node uint64, state string) Receipt {
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
		State:          state,
		ProbeDigest:    "probe",
		Sent:           10,
		Received:       10,
	}
}

func TestPlanAndRecord(t *testing.T) {	plan := samplePlan(t)
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	record := Record{
		Plan:      plan,
		Phase:     PhaseEffective,
		Watermark: plan.PairGen,
		Applied: [2]Receipt{
			sampleReceipt(plan, 10, StateApplied),
			sampleReceipt(plan, 20, StateApplied),
		},
		Verified: [2]Receipt{
			sampleReceipt(plan, 10, StateVerified),
			sampleReceipt(plan, 20, StateVerified),
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

func TestRecordReceiptBinding(t *testing.T) {
	plan := samplePlan(t)
	makeRecord := func() Record {
		return Record{
			Plan:      plan,
			Phase:     PhaseEffective,
			Watermark: plan.PairGen,
			Applied: [2]Receipt{
				sampleReceipt(plan, 10, StateApplied),
				sampleReceipt(plan, 20, StateApplied),
			},
			Verified: [2]Receipt{
				sampleReceipt(plan, 10, StateVerified),
				sampleReceipt(plan, 20, StateVerified),
			},
			EffectiveUntil: plan.ValidUntil,
		}
	}
	record := makeRecord()
	record.Applied[0].NodeID = 99
	if err := record.Validate(); err == nil {
		t.Fatal("accepted receipt from a node outside the plan sides")
	}
	record = makeRecord()
	record.Applied[0].NodeID = plan.Sides[1].NodeID
	if err := record.Validate(); err == nil {
		t.Fatal("accepted receipt bound to the wrong side")
	}
	record = makeRecord()
	record.Applied[0].State = StateVerified
	if err := record.Validate(); err == nil {
		t.Fatal("accepted VERIFIED receipt in the APPLIED slot")
	}
	record = makeRecord()
	record.Verified[0].State = StateApplied
	if err := record.Validate(); err == nil {
		t.Fatal("accepted APPLIED receipt in the VERIFIED slot")
	}
	record = makeRecord()
	record.Verified[0].Sent = 0
	if err := record.Validate(); err == nil {
		t.Fatal("accepted VERIFIED receipt without sent counters")
	}
	record = makeRecord()
	record.Verified[1].Received = 0
	if err := record.Validate(); err == nil {
		t.Fatal("accepted VERIFIED receipt without received counters")
	}
	record = makeRecord()
	record.EffectiveUntil = plan.ValidUntil + 1
	if err := record.Validate(); err == nil {
		t.Fatal("accepted effective window beyond plan validity")
	}
}

func TestPlanRejectsNonUnicastExport(t *testing.T) {
	for _, export := range []string{
		"0.0.0.0:14001",
		"[::]:14001",
		"224.0.0.1:14001",
		"[ff02::1]:14001",
	} {
		plan := Plan{
			OperationID:    "op-1",
			Scope:          "scope.test",
			PairID:         30,
			PairGen:        2,
			LeaseEpoch:     7,
			PlacementEpoch: 3,
			ValidUntil:     200,
			Sides: [2]Side{
				{NodeID: 10, Instance: "a", Export: export},
				{NodeID: 20, Instance: "b", Export: "127.0.0.1:14002"},
			},
		}
		digested, err := plan.WithDigest()
		if err != nil {
			t.Fatal(err)
		}
		if err := digested.Validate(); err == nil {
			t.Fatalf("export %q was accepted", export)
		}
	}
}
