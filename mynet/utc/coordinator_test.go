//go:build !windows

package utc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type testAuthority struct {
	calls       int
	revokeAfter int
}

func (a *testAuthority) Validate(ctx context.Context, p Plan) error {
	a.calls++
	if a.revokeAfter > 0 && a.calls >= a.revokeAfter {
		return errors.New("revoked")
	}
	return ctx.Err()
}

type testNode struct {
	now        uint64
	fail       string
	applyCalls int
	blockCalls int
	lastBlock  uint64
	store      Store
}

func (n *testNode) evidence(p Plan, i int, state string) Receipt {
	return Receipt{OperationID: p.OperationID, Scope: p.Scope, PairID: p.PairID, Generation: p.Generation, Manifest: p.Manifest, NodeID: p.Sides[i].NodeID, BootID: strings.Repeat(fmt.Sprint(i+1), 32), PID: uint64(i + 100), ObservedAt: n.now, ValidUntil: p.ValidUntil, State: state}
}
func (n *testNode) Prepare(ctx context.Context, p Plan, i int) (Receipt, error) {
	if n.fail == "prepare" {
		return Receipt{}, errors.New("prepare failed")
	}
	return n.evidence(p, i, "PREPARED"), nil
}
func (n *testNode) Apply(ctx context.Context, p Plan, i int, prepared Receipt) (Receipt, error) {
	n.applyCalls++
	if n.store != nil {
		r, err := n.store.Load(p.Scope, p.PairID)
		if err != nil || r.Phase != "APPLYING" {
			return Receipt{}, errors.New("intent not durable before apply")
		}
	}
	if n.fail == "apply" {
		return Receipt{}, context.DeadlineExceeded
	}
	r := n.evidence(p, i, "APPLIED")
	if n.fail == "oldgen" {
		r.Generation--
	}
	if n.fail == "oldboot" {
		r.BootID = strings.Repeat("a", 32)
	}
	return r, nil
}
func (n *testNode) Verify(ctx context.Context, p Plan, i int, applied [2]Receipt) (Receipt, error) {
	r := n.evidence(p, i, "VERIFIED")
	r.PeerBootID = applied[1-i].BootID
	r.Sent = 1
	r.Received = 1
	if n.fail == "probe" {
		r.Received = 0
	}
	if n.fail == "oldprobe" {
		r.OperationID = "previous"
	}
	if n.fail == "stale" {
		r.ObservedAt -= 6
	}
	return r, nil
}
func (n *testNode) Block(ctx context.Context, p Plan, i int) (Receipt, error) {
	n.blockCalls++
	n.lastBlock = p.Generation
	if n.fail == "block" {
		return Receipt{}, errors.New("offline")
	}
	return n.evidence(p, i, "BLOCKED"), nil
}
func testPlan() Plan {
	p := Plan{OperationID: "op1", Scope: "lab", PairID: 12, Generation: 1, ValidUntil: 2000, LeaseRefs: []string{"lease1"}, Sides: [2]Side{{NodeID: 1, Instance: "node1", Export: "127.0.0.1:14001"}, {NodeID: 2, Instance: "node2", Export: "192.0.2.1:14000"}}}
	p.Manifest = p.Digest()
	return p
}
func testCoordinator(t *testing.T) (*Coordinator, *testNode, *testNode) {
	t.Helper()
	store := DiskStore{Directory: t.TempDir()}
	a := &testNode{now: 1000, store: store}
	b := &testNode{now: 1000, store: store}
	c := &Coordinator{Store: store, Authority: &testAuthority{}, Nodes: map[uint64]Node{1: a, 2: b}, Now: func() time.Time { return time.Unix(1000, 0) }}
	return c, a, b
}
func TestCoordinatorPersistsEffectiveOnlyWithMatchingEvidence(t *testing.T) {
	c, a, b := testCoordinator(t)
	p := testPlan()
	record, err := c.Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if record.Phase != "EFFECTIVE" || record.EffectiveUntil != 1005 || a.applyCalls != 1 || b.applyCalls != 1 {
		t.Fatalf("unexpected result %+v", record)
	}
	stored, err := c.Store.Load(p.Scope, p.PairID)
	if err != nil || stored.Phase != "EFFECTIVE" {
		t.Fatalf("not durably confirmed %+v %v", stored, err)
	}
	// Confirmation retry is idempotent in generation, but must refresh evidence.
	if _, err := c.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	p.OperationID = "conflicting-operation"
	if _, err := c.Run(context.Background(), p); err == nil {
		t.Fatal("same pair generation rebound to new operation")
	}
}
func TestCoordinatorRejectsBadEvidenceAndCompensatesWithNewGeneration(t *testing.T) {
	for _, failure := range []string{"apply", "oldgen", "oldboot", "probe", "oldprobe", "stale"} {
		t.Run(failure, func(t *testing.T) {
			c, a, b := testCoordinator(t)
			b.fail = failure
			record, err := c.Run(context.Background(), testPlan())
			if err == nil {
				t.Fatal("failed operation reported effective")
			}
			if record.Phase != "BLOCKED" || record.Watermark != 2 || record.EffectiveUntil != 0 || a.lastBlock != 2 || b.lastBlock != 2 {
				t.Fatalf("unsafe compensation %+v", record)
			}
			applyCalls := a.applyCalls + b.applyCalls
			if _, err := c.Run(context.Background(), testPlan()); err == nil {
				t.Fatal("compensated operation revived")
			}
			if a.applyCalls+b.applyCalls != applyCalls {
				t.Fatal("old generation reapplied after compensation")
			}
		})
	}
}
func TestCoordinatorDoesNotClaimOfflineNodeBlocked(t *testing.T) {
	c, a, b := testCoordinator(t)
	a.fail = "probe"
	b.fail = "block"
	record, err := c.Run(context.Background(), testPlan())
	if err == nil || record.Phase != "BLOCK_EXPECTED" {
		t.Fatalf("offline endpoint fabricated as blocked %+v %v", record, err)
	}
	if record.Blocked[0].State != "BLOCKED" || record.Blocked[1].State != "" {
		t.Fatal("incorrect evidence aggregation")
	}
	b.fail = ""
	a.fail = ""
	record, err = c.Run(context.Background(), testPlan())
	if err == nil || record.Phase != "BLOCKED" {
		t.Fatalf("failed to finish compensation %+v %v", record, err)
	}
}
func TestCoordinatorPrepareFailureDoesNotMutateExistingPath(t *testing.T) {
	c, a, b := testCoordinator(t)
	b.fail = "prepare"
	record, err := c.Run(context.Background(), testPlan())
	if err == nil || record.Phase != "FAILED" {
		t.Fatal("prepare failure hidden")
	}
	if a.applyCalls+b.applyCalls+a.blockCalls+b.blockCalls != 0 {
		t.Fatal("prepare failure changed data path")
	}
}
func TestCoordinatorRestartDuringApplyFencesUncertainOldOperation(t *testing.T) {
	c, a, b := testCoordinator(t)
	p := testPlan()
	if err := c.Store.Save(Record{Plan: p, Phase: "APPLYING", Watermark: p.Generation}); err != nil {
		t.Fatal(err)
	}
	record, err := c.Run(context.Background(), p)
	if err == nil || record.Phase != "BLOCKED" || record.Watermark != 2 {
		t.Fatalf("unsafe interrupted recovery %+v %v", record, err)
	}
	if a.applyCalls+b.applyCalls != 0 {
		t.Fatal("uncertain original generation resumed")
	}
	next := p
	next.OperationID = "op3"
	next.Generation = 3
	next.Manifest = next.Digest()
	if record, err = c.Run(context.Background(), next); err != nil || record.Phase != "EFFECTIVE" {
		t.Fatalf("fresh generation did not recover %+v %v", record, err)
	}
	if _, err := c.Run(context.Background(), p); err == nil {
		t.Fatal("late old operation accepted")
	}
}

type failConfirmStore struct{ Store }

func (s failConfirmStore) Save(record Record) error {
	if record.Phase == "EFFECTIVE" {
		return errors.New("fsync failed")
	}
	return s.Store.Save(record)
}
func TestCoordinatorFailedDurableConfirmationIsNotEffective(t *testing.T) {
	c, _, _ := testCoordinator(t)
	c.Store = failConfirmStore{c.Store}
	record, err := c.Run(context.Background(), testPlan())
	if err == nil || record.Phase == "EFFECTIVE" || record.EffectiveUntil != 0 {
		t.Fatal("uncommitted success escaped")
	}
}
func TestCoordinatorRechecksRevocationBeforeForwarding(t *testing.T) {
	c, a, b := testCoordinator(t)
	c.Authority = &testAuthority{revokeAfter: 2}
	record, err := c.Run(context.Background(), testPlan())
	if err == nil || record.Phase != "BLOCKED" {
		t.Fatalf("revocation missed %+v %v", record, err)
	}
	if a.applyCalls+b.applyCalls != 0 {
		t.Fatal("forwarding changed after revocation")
	}
}
func TestDiskStoreRejectsCorruptionAndReleasesContextWait(t *testing.T) {
	store := DiskStore{Directory: t.TempDir()}
	p := testPlan()
	if err := store.Save(Record{Plan: p, Phase: "INTENT", Watermark: 1}); err != nil {
		t.Fatal(err)
	}
	path := store.path(p.Scope, p.PairID, ".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "INTENT", "FAILED", 1))
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(p.Scope, p.PairID); err == nil {
		t.Fatal("journal corruption accepted")
	}
	unlock, err := store.Lock(context.Background(), p.Scope, p.PairID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := store.Lock(ctx, p.Scope, p.PairID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock ignored context: %v", err)
	}
}

func TestCoordinatorStatusNeverKeepsStaleOrRevokedSuccess(t *testing.T) {
	c, _, _ := testCoordinator(t)
	p := testPlan()
	if _, err := c.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	c.Now = func() time.Time { return time.Unix(1006, 0) }
	record, err := c.Status(context.Background(), p.Scope, p.PairID)
	if err != nil || record.Phase != "DEGRADED" {
		t.Fatalf("stale success retained %+v %v", record, err)
	}
	c.Now = func() time.Time { return time.Unix(1000, 0) }
	c.Authority = &testAuthority{revokeAfter: 1}
	record, err = c.Status(context.Background(), p.Scope, p.PairID)
	if err != nil || record.Phase != "DEGRADED" {
		t.Fatalf("revoked success retained %+v %v", record, err)
	}
}

func TestCoordinatorReservesGenerationForCompensation(t *testing.T) {
	c, a, b := testCoordinator(t)
	p := testPlan()
	p.Generation = ^uint64(0)
	p.Manifest = p.Digest()
	if _, err := c.Run(context.Background(), p); err == nil {
		t.Fatal("uncompensatable forwarding generation accepted")
	}
	if a.applyCalls+b.applyCalls != 0 {
		t.Fatal("exhausted generation changed forwarding")
	}
}
