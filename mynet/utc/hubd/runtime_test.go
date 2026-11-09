package hubd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/utc/hub"
)

func coreConfig(t *testing.T, address string, public ed25519.PublicKey) Config {
	t.Helper()
	conf := t.TempDir()
	state := t.TempDir()
	return Config{
		HubAddress: address, Scope: testScope, TransportID: testTransport,
		AccountID: testAccount, IssuerPublicKey: public,
		Identity: hub.Identity{
			Scope: testScope, Instance: "instance-a", LocalID: testLocalID,
			BootID: testBoot, PID: 123,
			Capabilities: hub.CapabilityEpoch | hub.CapabilityExplicitActivate,
			ConfigGen:    5,
		},
		Pairs: []PairConfig{{
			PairID: testPairID, PeerID: testPeerID, Side: 0, Mode: hub.PeerModeAS,
			SummaryPath:  filepath.Join(state, "utc.hub.json"),
			VerifiedPath: filepath.Join(state, "utc.hub.json.verified"),
		}},
		ConfDir: conf, StatusPath: filepath.Join(conf, "utc.status"),
		LeaseTTL: 3600 * time.Second, RenewLead: 600 * time.Second,
		JitterMax: 60 * time.Second, StatusPoll: 10 * time.Millisecond,
		ApplyTimeout: 300 * time.Millisecond, RetryMin: 10 * time.Millisecond,
		RetryMax: 50 * time.Millisecond, StatusMaxAge: 5 * time.Second,
	}
}

func newRuntime(t *testing.T, config Config) *Runtime {
	t.Helper()
	runtime, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Jitter = func(time.Duration) time.Duration { return 0 }
	return runtime
}

func stepOnce(t *testing.T, runtime *Runtime) error {
	t.Helper()
	if err := runtime.load(); err != nil {
		return err
	}
	return runtime.step(context.Background())
}

func baseReply(t *testing.T, key ed25519.PrivateKey, pairGen, lease, placement, edge uint64,
	validUntil int64) testReply {
	t.Helper()
	now := time.Now().Unix()
	return testReplyFor(t, key, pairGen, lease, placement, edge, "203.0.113.7", 9443,
		uint64(now-10), uint64(validUntil))
}

func assignState(t *testing.T, key ed25519.PrivateKey) (Config, testReply) {
	t.Helper()
	config := coreConfig(t, "", key.Public().(ed25519.PublicKey))
	reply := baseReply(t, key, 1, 1, 1, 2, time.Now().Add(5*time.Minute).Unix())
	server := newFakeHub(t, func(_ *fakeHub, _ int, request recordedRequest) []byte {
		if request.u8(1) != 1 {
			t.Errorf("assign op = %d", request.u8(1))
		}
		writeStatus(t, config.StatusPath, "RUNNING", 5, 1, "APPLIED", "POLICY_LOADED")
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	if err := stepOnce(t, newRuntime(t, config)); err != nil {
		t.Fatalf("initial assign: %v", err)
	}
	return config, reply
}

func TestRuntimeAssignPublishesAndVerifies(t *testing.T) {
	key, public := testKey(t)
	config := coreConfig(t, "", public)
	reply := baseReply(t, key, 1, 1, 1, 2, time.Now().Add(5*time.Minute).Unix())
	server := newFakeHub(t, func(_ *fakeHub, _ int, _ recordedRequest) []byte {
		writeStatus(t, config.StatusPath, "RUNNING", 5, 1, "APPLIED", "POLICY_LOADED")
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	if err := stepOnce(t, runtime); err != nil {
		t.Fatal(err)
	}
	if server.count() != 1 {
		t.Fatalf("hub saw %d requests", server.count())
	}
	request := server.at(0)
	if request.u8(1) != 1 || request.text(2) != testScope ||
		request.text(7) != testTransport || request.text(8) != testAccount {
		t.Fatalf("assign request = %+v", request.fields)
	}
	raw := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	projection, err := hub.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	peer := projection.Peers[0]
	if projection.ConfigGen != 5 || peer.PairGen != 1 || peer.LeaseEpoch != 1 ||
		peer.PlacementEpoch != 1 || peer.Mode != 1 || peer.ExpiresAt != reply.grant.validUntil {
		t.Fatalf("projection = %+v peer=%+v", projection, peer)
	}
	summary, err := hub.LoadSummary(config.Pairs[0].SummaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PairGen != 1 || summary.ConfigGen != 5 || !summary.SignatureVerified {
		t.Fatalf("summary = %+v", summary)
	}
	verified, err := loadVerified(config.Pairs[0].VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	if verified.PairGen != 1 || verified.ConfigGen != 5 ||
		verified.ProjectionDigest != projectionDigest(raw) {
		t.Fatalf("verified = %+v", verified)
	}
	info, err := os.Stat(filepath.Join(config.ConfDir, "utc.conf"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("utc.conf mode = %v err=%v", info.Mode(), err)
	}
}

func TestRuntimeResumesFromSummary(t *testing.T) {
	key, _ := testKey(t)
	config, assigned := assignState(t, key)
	reply := baseReply(t, key, 1, 1, 1, 2, int64(assigned.grant.validUntil))
	server := newFakeHub(t, func(_ *fakeHub, _ int, request recordedRequest) []byte {
		if request.u8(1) != 2 {
			t.Errorf("restart op = %d, want renew", request.u8(1))
		}
		if _, rotated := request.fields[12]; rotated {
			t.Error("healthy APPLIED pair must not rotate")
		}
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	if err := stepOnce(t, runtime); err != nil {
		t.Fatal(err)
	}
	if server.count() != 1 || server.at(0).u8(1) != 2 {
		t.Fatalf("restart did not renew: count=%d", server.count())
	}
	if server.at(0).u64(10) != 1 || server.at(0).u64(11) != 1 {
		t.Fatalf("renew presented pair_gen=%d lease=%d", server.at(0).u64(10), server.at(0).u64(11))
	}
}

func TestRuntimeSameGenerationRenewalDoesNotRewriteProjection(t *testing.T) {
	key, _ := testKey(t)
	config, assigned := assignState(t, key)
	before := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	reply := baseReply(t, key, 1, 1, 1, 2, int64(assigned.grant.validUntil))
	server := newFakeHub(t, func(_ *fakeHub, _ int, request recordedRequest) []byte {
		if request.u8(1) != 2 {
			t.Errorf("renew op = %d", request.u8(1))
		}
		if _, rotated := request.fields[12]; rotated {
			t.Error("renewal rotated without a block")
		}
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	if err := stepOnce(t, runtime); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	if !bytes.Equal(before, after) {
		t.Fatal("same-generation renewal rewrote utc.conf")
	}
	summary, err := hub.LoadSummary(config.Pairs[0].SummaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ValidUntil != assigned.grant.validUntil {
		t.Fatalf("summary valid_until = %d, want %d", summary.ValidUntil, assigned.grant.validUntil)
	}
	verified, err := loadVerified(config.Pairs[0].VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := hub.Parse(after)
	if err != nil {
		t.Fatal(err)
	}
	if verified.ConfigGen != projection.ConfigGen || verified.ValidUntil != projection.Peers[0].ExpiresAt ||
		verified.PairGen != 1 {
		t.Fatalf("verified = %+v, applied expiry = %d", verified, projection.Peers[0].ExpiresAt)
	}
	if projection.ConfigGen != 5 {
		t.Fatalf("unchanged renewal bumped config_gen to %d", projection.ConfigGen)
	}
}

func TestRuntimeSameGenerationRenewalRepublishesLaterExpiry(t *testing.T) {
	key, _ := testKey(t)
	config, _ := assignState(t, key)
	later := time.Now().Add(50 * time.Minute).Unix()
	reply := baseReply(t, key, 1, 1, 1, 2, later)
	server := newFakeHub(t, func(_ *fakeHub, _ int, request recordedRequest) []byte {
		if request.u8(1) != 2 {
			t.Errorf("renew op = %d", request.u8(1))
		}
		if _, rotated := request.fields[12]; rotated {
			t.Error("renewal rotated without a block")
		}
		writeStatus(t, config.StatusPath, "RUNNING", 6, 1, "APPLIED", "POLICY_LOADED")
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	if err := stepOnce(t, runtime); err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	projection, err := hub.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	peer := projection.Peers[0]
	if projection.ConfigGen != 6 || peer.PairGen != 1 || peer.LeaseEpoch != 1 ||
		peer.ExpiresAt != uint64(later) {
		t.Fatalf("renewed projection gen=%d peer=%+v", projection.ConfigGen, peer)
	}
	summary, err := hub.LoadSummary(config.Pairs[0].SummaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ConfigGen != 6 || summary.ValidUntil != uint64(later) || summary.PairGen != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	verified, err := loadVerified(config.Pairs[0].VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	if verified.ConfigGen != 6 || verified.ValidUntil != uint64(later) ||
		verified.PairGen != 1 || verified.ProjectionDigest != projectionDigest(raw) {
		t.Fatalf("verified = %+v", verified)
	}
}

func TestRuntimeRepublishedRenewalWaitsForAPPLIED(t *testing.T) {
	key, _ := testKey(t)
	config, _ := assignState(t, key)
	before, err := loadVerified(config.Pairs[0].VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(50 * time.Minute).Unix()
	reply := baseReply(t, key, 1, 1, 1, 2, later)
	server := newFakeHub(t, func(_ *fakeHub, index int, _ recordedRequest) []byte {
		// GNB has not consumed config_gen 6 yet: its status header stays at 5.
		if index == 0 {
			writeStatus(t, config.StatusPath, "RUNNING", 5, 1, "APPLIED", "POLICY_LOADED")
		} else {
			writeStatus(t, config.StatusPath, "RUNNING", 6, 1, "APPLIED", "POLICY_LOADED")
		}
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	if err := runtime.load(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.step(context.Background()); err == nil {
		t.Fatal("renewal was accepted without APPLIED for the new config_gen")
	}
	after, err := loadVerified(config.Pairs[0].VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("verified watermark advanced before APPLIED: %+v", after)
	}
	if raw := readFile(t, filepath.Join(config.ConfDir, "utc.conf")); projectionConfigGen(t, raw) != 6 {
		t.Fatalf("renewed projection was not published: gen=%d", projectionConfigGen(t, raw))
	}
	if err := runtime.step(context.Background()); err != nil {
		t.Fatalf("retry after GNB applied config_gen 6: %v", err)
	}
	applied, err := loadVerified(config.Pairs[0].VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	if applied.ConfigGen != 6 || applied.ValidUntil != uint64(later) || applied.PairGen != 1 {
		t.Fatalf("verified after APPLIED = %+v", applied)
	}
}

func projectionConfigGen(t *testing.T, raw []byte) uint64 {
	t.Helper()
	projection, err := hub.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return projection.ConfigGen
}

func TestRuntimeRotatesOnRecoveredBlocked(t *testing.T) {
	key, _ := testKey(t)
	config, _ := assignState(t, key)
	writeStatus(t, config.StatusPath, "RECOVERED_BLOCKED", 5, 1, "BLOCKED", "REQUIRES_NEW_GENERATION")
	reply := baseReply(t, key, 2, 2, 1, 2, time.Now().Add(5*time.Minute).Unix())
	server := newFakeHub(t, func(_ *fakeHub, _ int, request recordedRequest) []byte {
		if request.u8(1) != 2 || request.u8(12) != 1 {
			t.Errorf("recovery op=%d rotate=%d", request.u8(1), request.u8(12))
		}
		if request.u64(10) != 1 || request.u64(11) != 1 {
			t.Errorf("recovery prior = %d/%d", request.u64(10), request.u64(11))
		}
		writeStatus(t, config.StatusPath, "RUNNING", 6, 2, "APPLIED", "POLICY_LOADED")
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	if err := stepOnce(t, runtime); err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	projection, err := hub.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	peer := projection.Peers[0]
	if projection.ConfigGen != 6 || peer.PairGen != 2 || peer.LeaseEpoch != 2 {
		t.Fatalf("rotated projection gen=%d peer=%+v", projection.ConfigGen, peer)
	}
	summary, err := hub.LoadSummary(config.Pairs[0].SummaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PairGen != 2 || summary.ConfigGen != 6 || summary.RecoveryReason != "RECOVERED_BLOCKED" {
		t.Fatalf("summary = %+v", summary)
	}
	verified, err := loadVerified(config.Pairs[0].VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	if verified.PairGen != 2 || verified.ConfigGen != 6 {
		t.Fatalf("verified = %+v", verified)
	}
}

func TestRuntimeTransportRetryReusesNonce(t *testing.T) {
	key, public := testKey(t)
	config := coreConfig(t, "", public)
	reply := baseReply(t, key, 1, 1, 1, 2, time.Now().Add(5*time.Minute).Unix())
	server := newFakeHub(t, func(hubServer *fakeHub, index int, _ recordedRequest) []byte {
		if index == 0 {
			return nil
		}
		writeStatus(t, config.StatusPath, "RUNNING", 5, 1, "APPLIED", "POLICY_LOADED")
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	if err := stepOnce(t, newRuntime(t, config)); err != nil {
		t.Fatalf("retry after a lost reply failed: %v", err)
	}
	if server.count() != 2 {
		t.Fatalf("hub saw %d requests, want 2", server.count())
	}
	first, second := server.at(0).u64(9), server.at(1).u64(9)
	if first == 0 || first != second {
		t.Fatalf("retry nonce first=%d second=%d", first, second)
	}
}

func TestRuntimeHubRefusalDoesNotPublish(t *testing.T) {
	_, public := testKey(t)
	config := coreConfig(t, "", public)
	server := newFakeHub(t, func(_ *fakeHub, _ int, _ recordedRequest) []byte {
		writer := newTestTLVWriter()
		if err := writer.addU16(tReasonTLV, 3); err != nil {
			t.Error(err)
		}
		return writer.buffer
	})
	config.HubAddress = server.listener.Addr().String()
	if err := stepOnce(t, newRuntime(t, config)); err == nil {
		t.Fatal("hub refusal was reported as success")
	}
	for _, path := range []string{
		filepath.Join(config.ConfDir, "utc.conf"),
		config.Pairs[0].SummaryPath,
		config.Pairs[0].VerifiedPath,
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("refusal wrote %s", path)
		}
	}
}

func TestRuntimeStatusNotAppliedIsNotSuccess(t *testing.T) {
	key, public := testKey(t)
	config := coreConfig(t, "", public)
	config.ApplyTimeout = 80 * time.Millisecond
	reply := baseReply(t, key, 1, 1, 1, 2, time.Now().Add(5*time.Minute).Unix())
	server := newFakeHub(t, func(_ *fakeHub, _ int, _ recordedRequest) []byte {
		writeStatus(t, config.StatusPath, "RUNNING", 5, 1, "BLOCKED", "EXPIRED")
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	err := stepOnce(t, runtime)
	if err == nil {
		t.Fatal("BLOCKED status was accepted as success")
	}
	if _, statErr := os.Stat(config.Pairs[0].VerifiedPath); !os.IsNotExist(statErr) {
		t.Fatal("verified watermark written without APPLIED")
	}
	if runtime.pairs[0].verified != nil {
		t.Fatal("runtime recorded a verified watermark without APPLIED")
	}
}

func TestRuntimeWriteFailureKeepsOldProjection(t *testing.T) {
	key, public := testKey(t)
	config := coreConfig(t, "", public)
	config.Identity.ConfigGen = 4
	now := time.Now()
	writeProjectionState(t, config, 4, 1, 1, 1, uint64(now.Add(5*time.Minute).Unix()))
	old := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	writeStatus(t, config.StatusPath, "RECOVERED_BLOCKED", 4, 1, "BLOCKED", "REQUIRES_NEW_GENERATION")
	if err := os.Chmod(config.ConfDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(config.ConfDir, 0700) })
	reply := baseReply(t, key, 2, 2, 1, 2, now.Add(5*time.Minute).Unix())
	server := newFakeHub(t, func(_ *fakeHub, _ int, _ recordedRequest) []byte {
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	err := stepOnce(t, runtime)
	if err == nil {
		t.Fatal("projection write failure was reported as success")
	}
	if !bytes.Equal(old, readFile(t, filepath.Join(config.ConfDir, "utc.conf"))) {
		t.Fatal("failed publish overwrote the old utc.conf")
	}
	if _, statErr := os.Stat(config.Pairs[0].VerifiedPath); !os.IsNotExist(statErr) {
		t.Fatal("verified watermark written after a failed publish")
	}
}

func TestRuntimeFailsClosedOnCorruptSummary(t *testing.T) {
	_, public := testKey(t)
	config := coreConfig(t, "127.0.0.1:1", public)
	if err := os.WriteFile(config.Pairs[0].SummaryPath, []byte(`{"scope":`), 0600); err != nil {
		t.Fatal(err)
	}
	server := newFakeHub(t, func(*fakeHub, int, recordedRequest) []byte { return nil })
	config.HubAddress = server.listener.Addr().String()
	runtime := newRuntime(t, config)
	if err := runtime.load(); err == nil {
		t.Fatal("corrupt summary was accepted")
	}
	if server.count() != 0 {
		t.Fatal("corrupt summary reached the hub; assign must not run")
	}
}

func TestRuntimeStopsOnContextCancel(t *testing.T) {
	_, public := testKey(t)
	config := coreConfig(t, "127.0.0.1:1", public)
	server := newFakeHub(t, func(*fakeHub, int, recordedRequest) []byte { return nil })
	config.HubAddress = server.listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := newRuntime(t, config).Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

func TestRuntimeRefreshesIdentityFromStatus(t *testing.T) {
	_, public := testKey(t)
	config := coreConfig(t, "127.0.0.1:1", public)
	config.Identity.BootID = "11111111111111111111111111111111"
	config.Identity.PID = 1
	config.Identity.ConfigGen = 3
	writeStatus(t, config.StatusPath, "RUNNING", 5, 1, "APPLIED", "POLICY_LOADED")
	runtime := newRuntime(t, config)
	runtime.refreshIdentity()
	if runtime.identity.BootID != testBoot || runtime.identity.PID != 123 ||
		runtime.configGen != 5 || runtime.pairs[0].session.Identity.BootID != testBoot {
		t.Fatalf("identity was not refreshed: %+v configGen=%d", runtime.identity, runtime.configGen)
	}
}

func TestNextDelayWaitsForPublishedExpiryInsideRenewalWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	pair := &pairState{
		grantValidUntil:    uint64(now.Unix() + 300),
		publishedExpiresAt: uint64(now.Unix() + 300),
	}
	runtime := &Runtime{
		cfg:    Config{RenewLead: 600 * time.Second, RetryMin: time.Second, JitterMax: 60 * time.Second},
		Now:    func() time.Time { return now },
		Jitter: func(time.Duration) time.Duration { return 0 },
	}
	if delay := runtime.nextDelay(pair); delay != 300*time.Second {
		t.Fatalf("inside-window delay = %v, want published expiry", delay)
	}
	pair.publishedExpiresAt = uint64(now.Unix() - 1)
	if delay := runtime.nextDelay(pair); delay != time.Second {
		t.Fatalf("expired delay = %v, want retry floor", delay)
	}
}

func TestFileNodeApplyRequiresAPPLIEDAndVerifyRefusesCounters(t *testing.T) {
	config := coreConfig(t, "127.0.0.1:1", nil)
	identity := hub.Identity{
		Scope: testScope, Instance: "instance-a", LocalID: testLocalID, PeerID: testPeerID,
		BootID: testBoot, PID: 123,
		Capabilities: hub.CapabilityEpoch | hub.CapabilityExplicitActivate,
		ConfigGen:    5,
	}
	validUntil := uint64(time.Now().Add(5 * time.Minute).Unix())
	plan := hub.Plan{
		OperationID: "op-1", Scope: testScope, PairID: testPairID,
		PairGen: 1, LeaseEpoch: 1, PlacementEpoch: 1, ValidUntil: validUntil,
		Sides: [2]hub.Side{
			{NodeID: testLocalID, Instance: "instance-a", Export: "127.0.0.1:14001"},
			{NodeID: testPeerID, Instance: "instance-b", Export: "203.0.113.7:9443"},
		},
	}
	plan, err := plan.WithDigest()
	if err != nil {
		t.Fatal(err)
	}
	node := &FileNode{
		Identity: identity, ConfDir: config.ConfDir,
		StatusPath: config.StatusPath, ApplyTimeout: 200 * time.Millisecond,
		Poll: 10 * time.Millisecond,
	}
	writeStatus(t, config.StatusPath, "RUNNING", 5, 1, "APPLIED", "POLICY_LOADED")
	receipt, err := node.Apply(context.Background(), plan, 0)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != hub.StateApplied || receipt.NodeID != testLocalID || receipt.PairGen != 1 {
		t.Fatalf("receipt = %+v", receipt)
	}
	raw := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	projection, err := hub.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Peers[0].PairGen != 1 || projection.Peers[0].Port != 9443 {
		t.Fatalf("node projection = %+v", projection.Peers[0])
	}
	if _, err := node.Verify(context.Background(), plan, 0); !errors.Is(err, ErrCountersUnavailable) {
		t.Fatalf("Verify error = %v, want ErrCountersUnavailable", err)
	}
	if _, err := node.Apply(context.Background(), plan, 1); err == nil {
		t.Fatal("node applied a side that is not local")
	}
}

func writeProjectionState(t *testing.T, config Config, configGen, pairGen, lease, placement uint64, validUntil uint64) {
	t.Helper()
	var address [16]byte
	address[10], address[11] = 0xff, 0xff
	copy(address[12:], []byte{203, 0, 113, 7})
	projection := hub.Projection{
		Scope: testScope, Instance: "instance-a", LocalID: testLocalID,
		ConfigGen: configGen, BootID: testBoot, PID: 123,
		Capabilities: hub.CapabilityEpoch | hub.CapabilityExplicitActivate,
		Peers: []hub.Peer{{
			PeerID: testPeerID, PairID: testPairID, PairGen: pairGen, Mode: 1,
			LeaseEpoch: lease, PlacementEpoch: placement, ExpiresAt: validUntil,
			Family: 4, Address: address, Port: 9443, Manifest: testManifest,
		}},
	}
	text, err := projection.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.ConfDir, "utc.conf"), text, 0600); err != nil {
		t.Fatal(err)
	}
	summary := hub.Summary{
		Scope: testScope, Instance: "instance-a", ConfigGen: configGen,
		TransportID: testTransport, AccountID: testAccount,
		PairID: testPairID, Side: 0, Mode: 1, PairGen: pairGen,
		LeaseEpoch: lease, PlacementEpoch: placement, EdgeID: 2,
		ValidUntil: validUntil, SignatureVerified: true,
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Pairs[0].SummaryPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A same-generation renewal that died between the utc.conf write and the
// verified-watermark write leaves the watermark one generation behind. That
// state is this runtime's own crash window, not corruption: a restart must
// load it and heal instead of refusing to start forever.
func TestRuntimeStaleVerifiedWatermarkSelfHeals(t *testing.T) {
	key, _ := testKey(t)
	config, _ := assignState(t, key)
	stale, err := loadVerified(config.Pairs[0].VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(50 * time.Minute).Unix()
	reply := baseReply(t, key, 1, 1, 1, 2, later)
	server := newFakeHub(t, func(_ *fakeHub, _ int, _ recordedRequest) []byte {
		writeStatus(t, config.StatusPath, "RUNNING", 6, 1, "APPLIED", "POLICY_LOADED")
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	if err := stepOnce(t, newRuntime(t, config)); err != nil {
		t.Fatalf("same-generation republish: %v", err)
	}
	republished := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	if projectionConfigGen(t, republished) != 6 {
		t.Fatalf("renewed projection gen=%d", projectionConfigGen(t, republished))
	}
	staleRaw, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.WriteFileAtomic(config.Pairs[0].VerifiedPath, staleRaw); err != nil {
		t.Fatal(err)
	}
	runtime := newRuntime(t, config)
	if err := runtime.load(); err != nil {
		t.Fatalf("stale verified watermark was refused as corruption: %v", err)
	}
	if err := runtime.step(context.Background()); err != nil {
		t.Fatalf("heal step: %v", err)
	}
	healed, err := loadVerified(config.Pairs[0].VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	if healed.ConfigGen != 6 || healed.PairGen != 1 ||
		healed.ProjectionDigest != projectionDigest(republished) {
		t.Fatalf("healed watermark = %+v", healed)
	}
}

// A utc.conf rewritten at the watermark's own generation with a different
// digest is a projection that was never applied: refuse it permanently.
func TestRuntimeFailsClosedOnProjectionMismatchAtWatermarkGeneration(t *testing.T) {
	key, _ := testKey(t)
	config, _ := assignState(t, key)
	raw := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	projection, err := hub.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	projection.Peers[0].Port++
	text, err := projection.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.WriteFileAtomic(filepath.Join(config.ConfDir, "utc.conf"), text); err != nil {
		t.Fatal(err)
	}
	err = newRuntime(t, config).load()
	if !errors.Is(err, errState) || !errors.Is(err, errProjectionMismatch) {
		t.Fatalf("tampered projection at the watermark generation was accepted: %v", err)
	}
}

// utc.conf may be lost while the summary survives. The GNB still holds the
// published config_gen as its high-water mark, so the rebuild must advance the
// generation instead of reusing the deleted file's generation.
func TestRuntimeRebuildAfterConfDeletionAdvancesGeneration(t *testing.T) {
	key, _ := testKey(t)
	config, _ := assignState(t, key)
	if err := os.Remove(filepath.Join(config.ConfDir, "utc.conf")); err != nil {
		t.Fatal(err)
	}
	reply := baseReply(t, key, 1, 1, 1, 2, time.Now().Add(5*time.Minute).Unix())
	server := newFakeHub(t, func(_ *fakeHub, _ int, _ recordedRequest) []byte {
		writeStatus(t, config.StatusPath, "RUNNING", 6, 1, "APPLIED", "POLICY_LOADED")
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	if err := stepOnce(t, newRuntime(t, config)); err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	if gen := projectionConfigGen(t, raw); gen != 6 {
		t.Fatalf("rebuilt projection reused config_gen %d", gen)
	}
}
