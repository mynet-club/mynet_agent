//go:build !windows

package utc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTopologyCoordinatorE2E drives the real coordinator against the public
// topology's live GNB processes and utcd control sockets, using the SAME plan
// and the SAME signed lease/revocation documents the active utcd sessions
// consume. TOPO_PLAN_EMIT writes the shared plan (manifest = plan digest) so
// the harness can sign its leases and GNB policy from it before starting the
// sessions; the run then loads exactly that plan and those documents.
func TestTopologyCoordinatorE2E(t *testing.T) {
	root := os.Getenv("TOPO_ROOT")
	if root == "" {
		t.Skip("TOPO_ROOT not set")
	}
	now := time.Now()
	ttl := uint64(250)
	if raw := os.Getenv("TOPO_PLAN_TTL"); raw != "" {
		if value, err := strconv.ParseUint(raw, 10, 64); err == nil && value > 0 {
			ttl = value
		}
	}
	exportA, exportB := "127.0.0.1:14001", "127.0.0.1:14002"
	if raw := os.Getenv("TOPO_PLAN_EXPORT_A"); raw != "" {
		exportA = raw
	}
	if raw := os.Getenv("TOPO_PLAN_EXPORT_B"); raw != "" {
		exportB = raw
	}
	plan := Plan{
		OperationID: fmt.Sprintf("topo-op-%d", now.Unix()),
		Scope:       "lab",
		PairID:      12,
		Generation:  1,
		ValidUntil:  uint64(now.Unix()) + ttl,
		LeaseRefs:   []string{"lease-A", "lease-B"},
		Sides: [2]Side{
			{NodeID: 1, Instance: "node1", Export: exportA},
			{NodeID: 2, Instance: "node2", Export: exportB},
		},
	}
	plan.Manifest = plan.Digest()
	planPath := os.Getenv("TOPO_PLAN_FILE")
	if planPath == "" {
		planPath = filepath.Join(root, "coordinator-plan.json")
	}
	if os.Getenv("TOPO_PLAN_EMIT") != "" {
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(planPath, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("COORDINATOR_PLAN %s\n", raw)
		return
	}
	loaded, err := LoadPlan(planPath)
	if err != nil {
		t.Fatalf("shared plan: %v", err)
	}
	plan = loaded

	// The authority is built around the very documents the live utcd sessions
	// consume: the harness's signed leases and revocation snapshot.
	pki := filepath.Join(root, "pki")
	dir := filepath.Join(root, "coordinator")
	authorityDir := filepath.Join(dir, "authority")
	leaseDir := filepath.Join(authorityDir, "leases")
	revocationDir := filepath.Join(authorityDir, "revocations")
	for _, path := range []string{leaseDir, revocationDir} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	leases := map[string]StaticLease{}
	bindings := []StaticBindingRule{}
	for _, ref := range plan.LeaseRefs {
		candidate := filepath.Join(pki, ref+".json")
		raw, err := os.ReadFile(candidate)
		if err != nil {
			candidate = filepath.Join(pki, strings.ToLower(ref)+".json")
			raw, err = os.ReadFile(candidate)
		}
		if err != nil {
			t.Fatalf("active lease %s: %v", ref, err)
		}
		var lease StaticLease
		if err := json.Unmarshal(raw, &lease); err != nil {
			t.Fatalf("active lease %s: %v", ref, err)
		}
		if lease.LeaseID != ref || lease.Scope != plan.Scope ||
			lease.PairID != plan.PairID || lease.PairGen != plan.Generation ||
			lease.Manifest != plan.Manifest {
			t.Fatalf("active lease %s does not match the shared plan: %+v", ref, lease)
		}
		if err := os.WriteFile(filepath.Join(leaseDir,
			StaticLeaseFile(plan.Scope, ref)), raw, 0600); err != nil {
			t.Fatal(err)
		}
		leases[ref] = lease
		bindings = append(bindings, StaticBindingRule{
			BindingID: lease.BindingID, Direction: lease.Direction,
			SubjectID: lease.SubjectID, SourceTransportID: lease.SourceTransportID,
			ProviderID: lease.ProviderID, TargetPeerID: lease.TargetPeerID,
			EndpointScope: lease.EndpointScope, QuotaAccountID: lease.QuotaAccountID,
			QuotaOwner: lease.QuotaOwner,
		})
	}
	rawRev, err := os.ReadFile(filepath.Join(pki, "revocations.json"))
	if err != nil {
		t.Fatalf("active revocations: %v", err)
	}
	var snapshot StaticRevocations
	if err := json.Unmarshal(rawRev, &snapshot); err != nil {
		t.Fatalf("active revocations: %v", err)
	}
	if snapshot.Scope != plan.Scope {
		t.Fatalf("revocation scope %q does not match the plan", snapshot.Scope)
	}
	keyID := snapshot.KeyID
	if err := os.WriteFile(filepath.Join(revocationDir,
		StaticRevocationFile(plan.Scope, keyID)), rawRev, 0600); err != nil {
		t.Fatal(err)
	}
	issuerKey := os.Getenv("TOPO_ISSUER_KEY_HEX")
	if issuerKey == "" {
		t.Fatal("TOPO_ISSUER_KEY_HEX is required")
	}
	public, err := hex.DecodeString(issuerKey)
	if err != nil || len(public) != 32 {
		t.Fatal("TOPO_ISSUER_KEY_HEX must be 32 raw bytes in hex")
	}
	sample := leases[plan.LeaseRefs[0]]
	config := StaticAuthorityConfig{
		LeaseDirectory: leaseDir, RevocationDirectory: revocationDir,
		StateDirectory: filepath.Join(authorityDir, "state"), Now: time.Now,
		Issuers: map[string]TrustedStaticIssuer{keyID: {
			IssuerID: sample.IssuerID, PublicKey: public,
			Scopes: []string{plan.Scope}, MinimumRevision: 1,
			MaxLeaseTTL: 86400, MaxRevocationTTL: 3600}},
		Pairs: []StaticPairRule{{Scope: plan.Scope, PairID: plan.PairID,
			Sides: plan.Sides, Bindings: bindings}},
	}
	authority, err := NewStaticAuthority(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.InstallRevocationFloor(context.Background(),
		plan.Scope, keyID, snapshot.Revision); err != nil {
		t.Fatal(err)
	}
	nodes := map[uint64]Node{}
	for i, side := range plan.Sides {
		nodes[side.NodeID] = &ExecNode{
			NodeID: side.NodeID, Instance: side.Instance,
			ConfDir:    filepath.Join(root, fmt.Sprint(side.NodeID)),
			GNBBinary:  os.Getenv("TOPO_GNB_BIN"),
			UTCDSocket: []string{os.Getenv("TOPO_SOCK_A"), os.Getenv("TOPO_SOCK_B")}[i],
		}
	}
	// A-S public topology: side B lives on the relay host, so it is driven over
	// SSH through the operator-installed remote CLI instead of a local ExecNode.
	if remoteHost := os.Getenv("TOPO_REMOTE_B"); remoteHost != "" && plan.Sides[1].NodeID != 0 {
		nodes[plan.Sides[1].NodeID] = &SSHNode{
			NodeID: plan.Sides[1].NodeID, Instance: plan.Sides[1].Instance,
			SSHHost: remoteHost, SSHKey: os.Getenv("TOPO_REMOTE_KEY"),
			RemoteConfDir: os.Getenv("TOPO_REMOTE_CONF"),
			RemoteCLI:     os.Getenv("TOPO_REMOTE_CLI"),
			RemoteGNB:     os.Getenv("TOPO_REMOTE_GNB"),
			RemoteUTCD:    os.Getenv("TOPO_REMOTE_UTCD"),
			RemoteSocket:  os.Getenv("TOPO_REMOTE_SOCK"),
		}
	}
	store := DiskStore{Directory: filepath.Join(dir, "store")}
	if os.Getenv("TOPO_DEBUG_APPLY") != "" {
		for i, side := range plan.Sides {
			prepared, prepErr := nodes[side.NodeID].Prepare(context.Background(), plan, i)
			t.Logf("side %d prepare: state=%s err=%v", i, prepared.State, prepErr)
			if prepErr == nil {
				applied, applyErr := nodes[side.NodeID].Apply(context.Background(), plan, i, prepared)
				t.Logf("side %d apply: state=%s err=%v", i, applied.State, applyErr)
			}
			conf := filepath.Join(root, fmt.Sprint(side.NodeID))
			if raw, readErr := os.ReadFile(filepath.Join(conf, "utc.status")); readErr == nil {
				t.Logf("side %d status:\n%s", i, raw)
			}
			if raw, readErr := os.ReadFile(filepath.Join(conf, "utc.conf")); readErr == nil {
				t.Logf("side %d conf:\n%s", i, raw)
			}
		}
		t.Fatalf("debug apply complete")
	}
	epochOf := func(fields map[string]string) uint64 {
		value, present := fields["connection_epoch"]
		if !present {
			t.Fatalf("utcd status lacks connection_epoch: %v", fields)
		}
		var epoch uint64
		if _, scanErr := fmt.Sscan(value, &epoch); scanErr != nil || epoch == 0 {
			t.Fatalf("utcd status has unusable connection_epoch %q", value)
		}
		return epoch
	}
	readSideEpoch := func(index int) uint64 {
		if sshn, ok := nodes[plan.Sides[index].NodeID].(*SSHNode); ok {
			fields, statusErr := sshn.utcdStatus()
			if statusErr != nil {
				t.Fatalf("remote utcd status: %v", statusErr)
			}
			return epochOf(fields)
		}
		fields, statusErr := UTCDStatus(context.Background(),
			[]string{os.Getenv("TOPO_SOCK_A"), os.Getenv("TOPO_SOCK_B")}[index])
		if statusErr != nil {
			t.Fatalf("utcd status: %v", statusErr)
		}
		return epochOf(fields)
	}
	epochsBefore := []uint64{readSideEpoch(0), readSideEpoch(1)}
	startedAt := time.Now()
	record, err := RunPlan(context.Background(), store, nodes, authority, plan)
	finishedAt := time.Now()
	if err != nil {
		t.Fatalf("coordinator run: %v (record phase %s)", err, record.Phase)
	}
	// The counters are instance-cumulative: they only belong to this operation
	// when both sides kept the same live session across it (strictly read, so
	// a missing field can never pass as 0 == 0).
	for i := range plan.Sides {
		if epoch := readSideEpoch(i); epoch != epochsBefore[i] {
			t.Fatalf("side %d session changed during the operation: %d -> %d",
				i, epochsBefore[i], epoch)
		}
	}
	if record.Phase != "EFFECTIVE" {
		t.Fatalf("coordinator phase = %s, want EFFECTIVE", record.Phase)
	}
	if record.Plan.OperationID != plan.OperationID || record.Plan.Manifest != plan.Manifest ||
		record.Plan.Generation != plan.Generation {
		t.Fatalf("record does not carry the shared plan: %+v", record.Plan)
	}
	for i := range plan.Sides {
		if record.Applied[i].State != "APPLIED" || record.Verified[i].State != "VERIFIED" {
			t.Fatalf("side %d receipts = %s/%s", i, record.Applied[i].State, record.Verified[i].State)
		}
		if record.Verified[i].Sent == 0 || record.Verified[i].Received == 0 {
			t.Fatalf("side %d verified counters = %d/%d", i, record.Verified[i].Sent, record.Verified[i].Received)
		}
		// The VERIFIED claim must name its own session and its own movement.
		if record.Verified[i].SessionEpoch == 0 ||
			record.Verified[i].SessionSent == 0 || record.Verified[i].SessionReceived == 0 {
			t.Fatalf("side %d verified counters are not session-attributed: %+v",
				i, record.Verified[i])
		}
	}
	out, _ := json.Marshal(record)
	fmt.Printf("COORDINATOR_RECORD %s\n", out)
	fmt.Printf("COORDINATOR_TIMING {\"started_at\":%d,\"finished_at\":%d}\n",
		startedAt.UnixNano(), finishedAt.UnixNano())
}
