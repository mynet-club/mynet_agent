//go:build !windows

package utc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type staticFixture struct {
	authority *StaticAuthority
	config    StaticAuthorityConfig
	plan      Plan
	leases    [2]StaticLease
	snapshot  StaticRevocations
	private   ed25519.PrivateKey
	now       uint64
}

func writeStaticJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *staticFixture) writeLease(t *testing.T, index int) {
	t.Helper()
	lease := &f.leases[index]
	payload, err := StaticLeaseSigningBytes(*lease)
	if err != nil {
		t.Fatal(err)
	}
	lease.Signature = hex.EncodeToString(ed25519.Sign(f.private, payload))
	writeStaticJSON(t, filepath.Join(f.config.LeaseDirectory, StaticLeaseFile(f.plan.Scope, lease.LeaseID)), lease)
}
func (f *staticFixture) writeSnapshot(t *testing.T) {
	t.Helper()
	payload, err := StaticRevocationSigningBytes(f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	f.snapshot.Signature = hex.EncodeToString(ed25519.Sign(f.private, payload))
	writeStaticJSON(t, filepath.Join(f.config.RevocationDirectory, StaticRevocationFile(f.plan.Scope, "key1")), f.snapshot)
}
func newStaticFixture(t *testing.T) *staticFixture {
	t.Helper()
	root := t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &staticFixture{private: private, now: 1000, plan: testPlan()}
	f.plan.ValidUntil = 1020
	f.plan.LeaseRefs = []string{"lease-ab", "lease-ba"}
	f.plan.Manifest = f.plan.Digest()
	f.config = StaticAuthorityConfig{LeaseDirectory: filepath.Join(root, "leases"), RevocationDirectory: filepath.Join(root, "revocations"), StateDirectory: filepath.Join(root, "state"), Now: func() time.Time { return time.Unix(int64(f.now), 0) }, Issuers: map[string]TrustedStaticIssuer{"key1": {IssuerID: "issuer", PublicKey: public, Scopes: []string{"lab"}, MinimumRevision: 1, MaxLeaseTTL: 300, MaxRevocationTTL: 60}}}
	for _, dir := range []string{f.config.LeaseDirectory, f.config.RevocationDirectory} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	var bindings []StaticBindingRule
	for i, direction := range []string{"a-to-b", "b-to-a"} {
		binding := StaticBindingRule{BindingID: "binding12", Direction: direction, SubjectID: "subjectA", SourceTransportID: "utcA", ProviderID: "edgeE", TargetPeerID: 2, EndpointScope: strings.Repeat("e", 64), QuotaAccountID: "account1", QuotaOwner: "edgeE"}
		bindings = append(bindings, binding)
		f.leases[i] = StaticLease{Version: 1, IssuerID: "issuer", KeyID: "key1", LeaseID: f.plan.LeaseRefs[i], Scope: "lab", SubjectID: binding.SubjectID, SourceTransportID: binding.SourceTransportID, ProviderID: binding.ProviderID, TargetPeerID: 2, PairID: 12, PairGen: 1, Manifest: f.plan.Manifest, BindingID: binding.BindingID, Direction: direction, EndpointScope: binding.EndpointScope, IssuedAt: 1000, NotBefore: 1000, ExpiresAt: 1100, ByteLimit: 10000, QuotaAccountID: binding.QuotaAccountID, QuotaOwner: binding.QuotaOwner, RevocationEpoch: 1, Nonce: strings.Repeat("a", 32)}
		f.writeLease(t, i)
	}
	f.config.Pairs = []StaticPairRule{{Scope: "lab", PairID: 12, Sides: f.plan.Sides, Bindings: bindings}}
	f.authority, err = NewStaticAuthority(f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.snapshot = StaticRevocations{Version: 1, Scope: f.plan.Scope, IssuerID: "issuer", KeyID: "key1", Revision: 1, IssuedAt: 1000, ExpiresAt: 1050}
	f.writeSnapshot(t)
	if err := f.authority.InstallRevocationFloor(context.Background(), f.plan.Scope, "key1", 1); err != nil {
		t.Fatal(err)
	}
	return f
}
func TestStaticAuthorityRealSignaturesAndReconnectAreIdempotent(t *testing.T) {
	f := newStaticFixture(t)
	for i := 0; i < 2; i++ {
		if err := f.authority.Validate(context.Background(), f.plan); err != nil {
			t.Fatal(err)
		}
	}
	// Trust is installed independently, not supplied by the lease. Caller changes
	// after construction cannot mutate live public keys/binding expectations.
	f.config.Issuers["key1"].PublicKey[0] ^= 0xff
	f.config.Pairs[0].Bindings[0].SubjectID = "changed-after-install"
	if err := f.authority.Validate(context.Background(), f.plan); err != nil {
		t.Fatal(err)
	}
}
func TestStaticAuthorityRejectsSignedScopeAndIdentityConfusion(t *testing.T) {
	cases := map[string]func(*StaticLease){
		"subject":          func(l *StaticLease) { l.SubjectID = "other" },
		"source_transport": func(l *StaticLease) { l.SourceTransportID = "other" },
		"provider":         func(l *StaticLease) { l.ProviderID = "other" },
		"target":           func(l *StaticLease) { l.TargetPeerID = 1 },
		"binding":          func(l *StaticLease) { l.BindingID = "other" },
		"direction":        func(l *StaticLease) { l.Direction = "b-to-a" },
		"endpoints":        func(l *StaticLease) { l.EndpointScope = strings.Repeat("f", 64) },
		"quota_owner":      func(l *StaticLease) { l.QuotaOwner = "other" },
		"quota_account":    func(l *StaticLease) { l.QuotaAccountID = "other" },
		"scope":            func(l *StaticLease) { l.Scope = "other" },
		"generation":       func(l *StaticLease) { l.PairGen = 2 },
		"manifest":         func(l *StaticLease) { l.Manifest = strings.Repeat("f", 64) },
		"future_epoch":     func(l *StaticLease) { l.RevocationEpoch = 2 },
		"not_yet":          func(l *StaticLease) { l.NotBefore = 1001 },
		"ttl":              func(l *StaticLease) { l.ExpiresAt = 1301 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newStaticFixture(t)
			change(&f.leases[0])
			f.writeLease(t, 0)
			if err := f.authority.Validate(context.Background(), f.plan); err == nil {
				t.Fatal("signed but unauthorized lease accepted")
			}
		})
	}
}
func TestStaticAuthorityRejectsForgeryAndNoncanonicalFiles(t *testing.T) {
	for _, kind := range []string{"signature", "unknown_key", "duplicate_key", "symlink", "mode", "version"} {
		t.Run(kind, func(t *testing.T) {
			f := newStaticFixture(t)
			path := filepath.Join(f.config.LeaseDirectory, StaticLeaseFile(f.plan.Scope, f.plan.LeaseRefs[0]))
			switch kind {
			case "signature":
				f.leases[0].Signature = strings.Repeat("0", 128)
				writeStaticJSON(t, path, f.leases[0])
			case "unknown_key":
				f.leases[0].KeyID = "unknown"
				f.writeLease(t, 0)
			case "duplicate_key":
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				raw = []byte(strings.Replace(string(raw), "{\"version\":1,", "{\"version\":1,\"version\":1,", 1))
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".old", path); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "version":
				f.leases[0].Version = 2
				writeStaticJSON(t, path, f.leases[0])
			}
			if err := f.authority.Validate(context.Background(), f.plan); err == nil {
				t.Fatal("forged/noncanonical lease accepted")
			}
		})
	}
}
func TestStaticAuthorityRevocationPersistsAcrossRestartAndRejectsRollback(t *testing.T) {
	f := newStaticFixture(t)
	f.snapshot.Revision = 2
	f.snapshot.RevokedLeaseIDs = []string{f.plan.LeaseRefs[0]}
	f.writeSnapshot(t)
	if err := f.authority.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("revoked lease accepted")
	}
	fresh, err := NewStaticAuthority(f.config)
	if err != nil {
		t.Fatal(err)
	}
	f.snapshot.Revision = 1
	f.snapshot.RevokedLeaseIDs = nil
	f.writeSnapshot(t)
	if err := fresh.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("persisted revocation rollback accepted")
	}
}
func TestStaticAuthoritySameRevisionConflictDoesNotReplaceTrustedSnapshot(t *testing.T) {
	f := newStaticFixture(t)
	f.snapshot.RevokedLeaseIDs = []string{f.plan.LeaseRefs[0]}
	f.writeSnapshot(t)
	if err := f.authority.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("same revision conflict accepted")
	}
	f.snapshot.RevokedLeaseIDs = nil
	f.writeSnapshot(t)
	if err := f.authority.Validate(context.Background(), f.plan); err != nil {
		t.Fatalf("conflict poisoned trusted floor: %v", err)
	}
}
func TestStaticAuthorityMissingHistoryNeverBootstrapsOnValidate(t *testing.T) {
	f := newStaticFixture(t)
	if err := os.Remove(f.authority.floorPath(f.plan.Scope, "key1")); err != nil {
		t.Fatal(err)
	}
	if err := f.authority.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("missing watermark reset implicitly")
	}
	if _, err := os.Stat(f.authority.floorPath(f.plan.Scope, "key1")); !os.IsNotExist(err) {
		t.Fatal("validate created a new watermark")
	}
	if err := f.authority.InstallRevocationFloor(context.Background(), f.plan.Scope, "key1", 2); err == nil {
		t.Fatal("wrong explicit provisioning revision accepted")
	}
	if err := f.authority.InstallRevocationFloor(context.Background(), f.plan.Scope, "key1", 1); err != nil {
		t.Fatal(err)
	}
}
func TestStaticAuthorityTimeAndRevocationExpiryNeverRecoverByClockRewind(t *testing.T) {
	f := newStaticFixture(t)
	f.now = 1050 // exact snapshot expiry
	if err := f.authority.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("expired snapshot accepted")
	}
	f.now = 1000
	fresh, err := NewStaticAuthority(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("clock rollback revived expired authorization")
	}
}
func TestStaticAuthorityRejectsCorruptOrPublicState(t *testing.T) {
	for _, kind := range []string{"checksum", "directory"} {
		t.Run(kind, func(t *testing.T) {
			f := newStaticFixture(t)
			if kind == "directory" {
				if err := os.Chmod(f.config.StateDirectory, 0777); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(f.config.StateDirectory, 0700)
			} else {
				floor, err := f.authority.readFloor(f.plan.Scope, "key1", f.config.Issuers["key1"])
				if err != nil {
					t.Fatal(err)
				}
				floor.ClockFloor++
				writeStaticJSON(t, f.authority.floorPath(f.plan.Scope, "key1"), floor)
			}
			if err := f.authority.Validate(context.Background(), f.plan); err == nil {
				t.Fatal("unsafe authority state accepted")
			}
		})
	}
}

func TestStaticAuthorityConcurrentValidationUsesTimeAfterIssuerLock(t *testing.T) {
	f := newStaticFixture(t)
	f.plan.ValidUntil = 2100
	f.plan.Manifest = f.plan.Digest()
	for i := range f.leases {
		f.leases[i].IssuedAt = 1900
		f.leases[i].NotBefore = 1900
		f.leases[i].ExpiresAt = 2200
		f.leases[i].Manifest = f.plan.Manifest
		f.writeLease(t, i)
	}
	f.snapshot.Revision = 2
	f.snapshot.IssuedAt = 1900
	f.snapshot.ExpiresAt = 2200
	f.writeSnapshot(t)
	issuer := f.config.Issuers["key1"]
	issuer.MaxRevocationTTL = 300
	f.config.Issuers["key1"] = issuer
	var clock atomic.Uint64
	clock.Store(2000)
	f.config.Now = func() time.Time { return time.Unix(int64(clock.Add(1)), 0) }
	authority, err := NewStaticAuthority(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.InstallRevocationFloor(context.Background(), f.plan.Scope, "key1", 2); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- authority.Validate(context.Background(), f.plan) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("valid concurrent authorization rejected: %v", err)
		}
	}
}
func TestStaticAuthorityFinalSnapshotExpiryIsDurablyRemembered(t *testing.T) {
	f := newStaticFixture(t)
	f.plan.ValidUntil = 1100
	f.plan.Manifest = f.plan.Digest()
	for i := range f.leases {
		f.leases[i].IssuedAt = 900
		f.leases[i].NotBefore = 900
		f.leases[i].ExpiresAt = 1200
		f.leases[i].Manifest = f.plan.Manifest
		f.writeLease(t, i)
	}
	calls := 0
	f.authority.now = func() time.Time {
		calls++
		if calls >= 5 {
			return time.Unix(1050, 0)
		}
		return time.Unix(1000, 0)
	}
	if err := f.authority.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("snapshot expired during validation was accepted")
	}
	f.now = 1000
	fresh, err := NewStaticAuthority(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("restart forgot observed expiry")
	}
}
func TestCoordinatorRejectsRealSignedAuthorizationBeforeNodeMutation(t *testing.T) {
	f := newStaticFixture(t)
	f.leases[0].ProviderID = "wrong-provider"
	f.writeLease(t, 0)
	coordinator, a, b := testCoordinator(t)
	coordinator.Authority = f.authority
	record, err := coordinator.Run(context.Background(), f.plan)
	if err == nil || record.Reason != "AUTHORIZATION_REJECTED" {
		t.Fatalf("signed authority gate bypassed %+v %v", record, err)
	}
	if a.applyCalls+b.applyCalls+a.blockCalls+b.blockCalls != 0 {
		t.Fatal("invalid signed authority changed nodes")
	}
}

func TestStaticLeaseSignatureCoversBudgetAndNonce(t *testing.T) {
	for _, field := range []string{"budget", "nonce"} {
		t.Run(field, func(t *testing.T) {
			f := newStaticFixture(t)
			if field == "budget" {
				f.leases[0].ByteLimit++
			} else {
				f.leases[0].Nonce = strings.Repeat("b", 32)
			}
			writeStaticJSON(t, filepath.Join(f.config.LeaseDirectory, StaticLeaseFile(f.plan.Scope, f.plan.LeaseRefs[0])), f.leases[0])
			if err := f.authority.Validate(context.Background(), f.plan); err == nil {
				t.Fatal("unsigned lease mutation accepted")
			}
		})
	}
}
func TestStaticRevocationSignaturePreventsRemovingRevokedLease(t *testing.T) {
	f := newStaticFixture(t)
	f.snapshot.Revision = 2
	f.snapshot.RevokedLeaseIDs = []string{f.plan.LeaseRefs[0]}
	f.writeSnapshot(t)
	if err := f.authority.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("revoked lease accepted")
	}
	// Preserve the signature but remove the denied ID from the signed payload.
	f.snapshot.RevokedLeaseIDs = nil
	writeStaticJSON(t, filepath.Join(f.config.RevocationDirectory, StaticRevocationFile(f.plan.Scope, "key1")), f.snapshot)
	if err := f.authority.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("unsigned revocation removal accepted")
	}
}

func TestCoordinatorConsumesSignedAuthorityWithControlledNodeAdapters(t *testing.T) {
	f := newStaticFixture(t)
	coordinator, _, _ := testCoordinator(t)
	coordinator.Authority = f.authority
	record, err := coordinator.Run(context.Background(), f.plan)
	if err != nil || record.Phase != "EFFECTIVE" {
		t.Fatalf("signed authority failed coordinator contract %+v %v", record, err)
	}
	// This is the coordinator/crypto contract only; node evidence is controlled.
}

func TestStaticAuthorityHigherSnapshotCannotReviveKnownRevocation(t *testing.T) {
	f := newStaticFixture(t)
	f.snapshot.Revision = 2
	f.snapshot.RevokedLeaseIDs = []string{f.plan.LeaseRefs[0]}
	f.writeSnapshot(t)
	if err := f.authority.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("revocation was not applied")
	}
	f.snapshot.Revision = 3
	f.snapshot.RevokedLeaseIDs = nil
	f.writeSnapshot(t)
	fresh, err := NewStaticAuthority(f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("newer omission revived revoked lease")
	}
	floor, err := fresh.readFloor(f.plan.Scope, "key1", f.config.Issuers["key1"])
	if err != nil {
		t.Fatal(err)
	}
	if floor.Revision != 3 || len(floor.RevokedLeaseIDs) != 1 || floor.RevokedLeaseIDs[0] != f.plan.LeaseRefs[0] {
		t.Fatalf("revocation history lost %+v", floor)
	}
}
func TestStaticAuthorityRememberedRevocationsHaveHardCapacity(t *testing.T) {
	f := newStaticFixture(t)
	floor, err := f.authority.readFloor(f.plan.Scope, "key1", f.config.Issuers["key1"])
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1024; i++ {
		floor.RevokedLeaseIDs = append(floor.RevokedLeaseIDs, fmt.Sprintf("old-%04d", i))
	}
	if err := f.authority.saveFloor(floor); err != nil {
		t.Fatal(err)
	}
	f.snapshot.Revision = 2
	f.snapshot.RevokedLeaseIDs = []string{"new-id"}
	f.writeSnapshot(t)
	if err := f.authority.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("revocation history silently truncated")
	}
	floor, err = f.authority.readFloor(f.plan.Scope, "key1", f.config.Issuers["key1"])
	if err != nil {
		t.Fatal(err)
	}
	if len(floor.RevokedLeaseIDs) != 1024 || floor.Revision != 1 {
		t.Fatal("capacity failure destroyed previous history")
	}
}
func TestStaticAuthorityRevocationScopesAreIndependent(t *testing.T) {
	f := newStaticFixture(t)
	issuer := f.config.Issuers["key1"]
	issuer.Scopes = []string{"lab", "prod"}
	f.config.Issuers["key1"] = issuer
	rule := f.config.Pairs[0]
	rule.Scope = "prod"
	rule.PairID = 34
	rule.Sides = [2]Side{{NodeID: 3, Instance: "node3", Export: "127.0.0.1:15001"}, {NodeID: 4, Instance: "node4", Export: "192.0.2.1:15000"}}
	rule.Bindings = append([]StaticBindingRule(nil), rule.Bindings...)
	for i := range rule.Bindings {
		rule.Bindings[i].BindingID = "binding34"
		rule.Bindings[i].TargetPeerID = 4
	}
	f.config.Pairs = append(f.config.Pairs, rule)
	authority, err := NewStaticAuthority(f.config)
	if err != nil {
		t.Fatal(err)
	}
	prod := f.plan
	prod.Scope = rule.Scope
	prod.PairID = rule.PairID
	prod.Sides = rule.Sides
	prod.Manifest = prod.Digest()
	for i := range f.leases {
		lease := f.leases[i]
		lease.Scope = prod.Scope
		lease.PairID = prod.PairID
		lease.Manifest = prod.Manifest
		lease.BindingID = "binding34"
		lease.TargetPeerID = 4
		payload, err := StaticLeaseSigningBytes(lease)
		if err != nil {
			t.Fatal(err)
		}
		lease.Signature = hex.EncodeToString(ed25519.Sign(f.private, payload))
		writeStaticJSON(t, filepath.Join(f.config.LeaseDirectory, StaticLeaseFile(prod.Scope, lease.LeaseID)), lease)
	}
	snapshot := f.snapshot
	snapshot.Scope = prod.Scope
	payload, err := StaticRevocationSigningBytes(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Signature = hex.EncodeToString(ed25519.Sign(f.private, payload))
	prodFile := filepath.Join(f.config.RevocationDirectory, StaticRevocationFile(prod.Scope, "key1"))
	writeStaticJSON(t, prodFile, snapshot)
	if err := authority.InstallRevocationFloor(context.Background(), prod.Scope, "key1", 1); err != nil {
		t.Fatal(err)
	}
	f.snapshot.Revision = 2
	f.snapshot.RevokedLeaseIDs = []string{f.plan.LeaseRefs[0]}
	f.writeSnapshot(t)
	if err := authority.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("lab revocation missed")
	}
	if err := authority.Validate(context.Background(), prod); err != nil {
		t.Fatalf("lab revocation leaked into prod: %v", err)
	}
	// A valid signature from another scope cannot advance this scope's floor.
	writeStaticJSON(t, prodFile, f.snapshot)
	if err := authority.Validate(context.Background(), prod); err == nil {
		t.Fatal("cross-scope snapshot accepted")
	}
	floor, err := authority.readFloor(prod.Scope, "key1", issuer)
	if err != nil {
		t.Fatal(err)
	}
	if floor.Revision != 1 || len(floor.RevokedLeaseIDs) != 0 {
		t.Fatal("cross-scope snapshot poisoned state")
	}
}

func TestStaticSigningDomainsAndEmptyRevocationEncoding(t *testing.T) {
	f := newStaticFixture(t)
	lease, err := StaticLeaseSigningBytes(f.leases[0])
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := StaticRevocationSigningBytes(f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(lease, append([]byte("mynet.utc.static-lease.v1"), byte(0))) || !bytes.HasPrefix(snapshot, append([]byte("mynet.utc.static-revocations.v1"), byte(0))) {
		t.Fatal("signing domains must include one NUL separator")
	}
	f.snapshot.RevokedLeaseIDs = []string{}
	other, err := StaticRevocationSigningBytes(f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot, other) {
		t.Fatal("empty list has multiple signing representations")
	}
	path := filepath.Join(f.config.RevocationDirectory, StaticRevocationFile(f.plan.Scope, "key1"))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"revoked_lease_ids":[]`), []byte(`"revoked_lease_ids":null`), 1)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.authority.Validate(context.Background(), f.plan); err == nil {
		t.Fatal("noncanonical null revocation list accepted")
	}
}
