//go:build !windows

package mynet

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/utc"
)

func TestUTCAuthorityCLIUsesRealSignedLeasesAndExplicitFloor(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	leases, revocations, state := filepath.Join(root, "leases"), filepath.Join(root, "revocations"), filepath.Join(root, "state")
	for _, dir := range []string{leases, revocations} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := uint64(time.Now().Unix())
	plan := utc.Plan{OperationID: "cli-op", Scope: "cli", PairID: 12, Generation: 1, ValidUntil: now + 120, LeaseRefs: []string{"lease-ab", "lease-ba"}, Sides: [2]utc.Side{{NodeID: 1, Instance: "node1", Export: "127.0.0.1:14001"}, {NodeID: 2, Instance: "node2", Export: "192.0.2.1:14000"}}}
	plan.Manifest = plan.Digest()
	write := func(path string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var bindings []utc.StaticBindingRule
	var issued []utc.StaticLease
	for i, direction := range []string{"a-to-b", "b-to-a"} {
		binding := utc.StaticBindingRule{BindingID: "binding12", Direction: direction, SubjectID: "subjectA", SourceTransportID: "utcA", ProviderID: "edgeE", TargetPeerID: 2, EndpointScope: strings.Repeat("e", 64), QuotaAccountID: "account1", QuotaOwner: "edgeE"}
		bindings = append(bindings, binding)
		lease := utc.StaticLease{Version: 1, IssuerID: "issuer", KeyID: "key1", LeaseID: plan.LeaseRefs[i], Scope: plan.Scope, SubjectID: binding.SubjectID, SourceTransportID: binding.SourceTransportID, ProviderID: binding.ProviderID, TargetPeerID: 2, PairID: 12, PairGen: 1, Manifest: plan.Manifest, BindingID: binding.BindingID, Direction: direction, EndpointScope: binding.EndpointScope, IssuedAt: now - 1, NotBefore: now - 1, ExpiresAt: now + 300, ByteLimit: 10000, QuotaAccountID: binding.QuotaAccountID, QuotaOwner: binding.QuotaOwner, RevocationEpoch: 1, Nonce: strings.Repeat("a", 32)}
		payload, err := utc.StaticLeaseSigningBytes(lease)
		if err != nil {
			t.Fatal(err)
		}
		lease.Signature = hex.EncodeToString(ed25519.Sign(private, payload))
		write(filepath.Join(leases, utc.StaticLeaseFile(plan.Scope, lease.LeaseID)), lease)
		issued = append(issued, lease)
	}
	snapshot := utc.StaticRevocations{Version: 1, Scope: plan.Scope, IssuerID: "issuer", KeyID: "key1", Revision: 1, IssuedAt: now - 1, ExpiresAt: now + 60}
	payload, err := utc.StaticRevocationSigningBytes(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Signature = hex.EncodeToString(ed25519.Sign(private, payload))
	write(filepath.Join(revocations, utc.StaticRevocationFile(plan.Scope, "key1")), snapshot)
	trust := utc.StaticTrustFile{Version: 1, LeaseDirectory: leases, RevocationDirectory: revocations, StateDirectory: state, Issuers: map[string]utc.StaticTrustIssuer{"key1": {IssuerID: "issuer", PublicKey: hex.EncodeToString(public), Scopes: []string{plan.Scope}, MinimumRevision: 1, MaxLeaseTTL: 3600, MaxRevocationTTL: 300}}, Pairs: []utc.StaticPairRule{{Scope: plan.Scope, PairID: plan.PairID, Sides: plan.Sides, Bindings: bindings}}}
	trustFile, planFile := filepath.Join(root, "trust.json"), filepath.Join(root, "plan.json")
	write(trustFile, trust)
	write(planFile, plan)
	verify := []string{"utc", "authority", "verify", "--trust", trustFile, "--plan", planFile}
	if rc := Run(verify); rc == 0 {
		t.Fatal("missing floor initialized implicitly")
	}
	if rc := Run([]string{"utc", "authority", "install-floor", "--trust", trustFile, "--scope", plan.Scope, "--key", "key1", "--revision", "1"}); rc != 0 {
		t.Fatalf("explicit floor install rc=%d", rc)
	}
	var rc int
	output := captureStdoutFn(t, func() { rc = Run(verify) })
	if rc != 0 {
		t.Fatalf("signed lease verification rc=%d", rc)
	}
	var result struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil || result.State != "AUTHORIZED" {
		t.Fatalf("invalid authority result %q %v", output, err)
	}
	if strings.Contains(output, "EFFECTIVE") || strings.Contains(output, "APPLIED") {
		t.Fatal("authorization misrepresented as path state")
	}
	// Even another valid signature from the same issuer cannot change bindings.
	issued[0].ProviderID = "different-provider"
	payload, err = utc.StaticLeaseSigningBytes(issued[0])
	if err != nil {
		t.Fatal(err)
	}
	issued[0].Signature = hex.EncodeToString(ed25519.Sign(private, payload))
	write(filepath.Join(leases, utc.StaticLeaseFile(plan.Scope, issued[0].LeaseID)), issued[0])
	if rc := Run(verify); rc == 0 {
		t.Fatal("signed wrong provider accepted by CLI")
	}
}
