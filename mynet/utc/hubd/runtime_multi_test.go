package hubd

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/utc/hub"
)

func writeMultiStatus(t *testing.T, path, headerReason string, configGen uint64, rows ...string) {
	t.Helper()
	text := fmt.Sprintf("status-v1 %s instance-a %d %s %d %d %s %d\n"+
		"metrics 0 0 0 0\n",
		testScope, testLocalID, testBoot, configGen, time.Now().Unix(), headerReason, 123)
	for _, row := range rows {
		text += row + " " + testManifest + " " + testLocalDigest + "\n"
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func peerStatusRow(peerID, pairID, pairGen uint64, state, reason string) string {
	return fmt.Sprintf("peer %d %d %d %s %s", peerID, pairID, pairGen, state, reason)
}

func twoPairConfig(t *testing.T, key ed25519.PrivateKey) (Config, testReply, testReply) {
	t.Helper()
	config := coreConfig(t, "", key.Public().(ed25519.PublicKey))
	second := config.Pairs[0]
	second.PairID, second.PeerID = 31, 21
	second.SummaryPath = filepath.Join(t.TempDir(), "pair2.json")
	second.VerifiedPath = second.SummaryPath + ".verified"
	config.Pairs = append(config.Pairs, second)

	first := baseReply(t, key, 1, 1, 1, 2, time.Now().Add(5*time.Minute).Unix())
	secondReply := baseReply(t, key, 1, 1, 1, 3, time.Now().Add(5*time.Minute).Unix())
	secondReply.grant.pairID, secondReply.binding.pairID = 31, 31
	secondReply.binding.address, secondReply.binding.port = "203.0.113.8", 9444
	return config, first, secondReply
}

func assignTwoPairs(t *testing.T, key ed25519.PrivateKey) (Config, testReply, testReply) {
	t.Helper()
	config, first, second := twoPairConfig(t, key)
	server := newFakeHub(t, func(_ *fakeHub, _ int, request recordedRequest) []byte {
		rows := []string{
			peerStatusRow(testPeerID, testPairID, 1, "APPLIED", "POLICY_LOADED"),
			peerStatusRow(21, 31, 1, "APPLIED", "POLICY_LOADED"),
		}
		gen := uint64(5)
		reply := first
		if request.u64(3) == 31 {
			gen, reply = 6, second
		}
		writeMultiStatus(t, config.StatusPath, "RUNNING", gen, rows...)
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	if err := stepOnce(t, newRuntime(t, config)); err != nil {
		t.Fatalf("two-pair assign: %v", err)
	}
	return config, first, second
}

func TestRuntimeMultiPairAssignsAndPublishesBoth(t *testing.T) {
	key, _ := testKey(t)
	config, first, second := assignTwoPairs(t, key)

	raw := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	projection, err := hub.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if projection.ConfigGen != 6 || len(projection.Peers) != 2 {
		t.Fatalf("projection gen=%d peers=%+v", projection.ConfigGen, projection.Peers)
	}
	byPair := map[uint64]hub.Peer{}
	for _, peer := range projection.Peers {
		byPair[peer.PairID] = peer
	}
	if byPair[testPairID].PeerID != testPeerID || byPair[31].PeerID != 21 {
		t.Fatalf("peers = %+v", projection.Peers)
	}
	if byPair[testPairID].ExpiresAt != first.grant.validUntil ||
		byPair[31].ExpiresAt != second.grant.validUntil {
		t.Fatalf("peer deadlines = %+v", byPair)
	}

	firstSummary, err := hub.LoadSummary(config.Pairs[0].SummaryPath)
	if err != nil {
		t.Fatal(err)
	}
	secondSummary, err := hub.LoadSummary(config.Pairs[1].SummaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if firstSummary.ConfigGen != 5 || secondSummary.ConfigGen != 6 {
		t.Fatalf("summary gens = %d/%d", firstSummary.ConfigGen, secondSummary.ConfigGen)
	}
	firstVerified, err := loadVerified(config.Pairs[0].VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	secondVerified, err := loadVerified(config.Pairs[1].VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	if firstVerified.ConfigGen != 5 || secondVerified.ConfigGen != 6 ||
		secondVerified.ProjectionDigest != projectionDigest(raw) {
		t.Fatalf("verified watermarks = %+v / %+v", firstVerified, secondVerified)
	}
}

func TestRuntimeMultiPairFailureIsolatesOtherPair(t *testing.T) {
	key, _ := testKey(t)
	config, _, second := assignTwoPairs(t, key)
	later := time.Now().Add(50 * time.Minute).Unix()

	server := newFakeHub(t, func(_ *fakeHub, _ int, request recordedRequest) []byte {
		if request.u64(3) == 31 {
			writer := newTestTLVWriter()
			if err := writer.addU16(tReasonTLV, 3); err != nil {
				t.Error(err)
			}
			return writer.buffer
		}
		reply := baseReply(t, key, 1, 1, 1, 2, later)
		reply.grant.pairID, reply.binding.pairID = testPairID, testPairID
		writeMultiStatus(t, config.StatusPath, "RUNNING", 7,
			peerStatusRow(testPeerID, testPairID, 1, "APPLIED", "POLICY_LOADED"),
			peerStatusRow(21, 31, 1, "APPLIED", "POLICY_LOADED"))
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()

	runtime := newRuntime(t, config)
	if err := runtime.load(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.step(context.Background()); err == nil {
		t.Fatal("refused pair was reported as success")
	}

	raw := readFile(t, filepath.Join(config.ConfDir, "utc.conf"))
	projection, err := hub.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if projection.ConfigGen != 7 || len(projection.Peers) != 2 {
		t.Fatalf("projection gen=%d peers=%+v", projection.ConfigGen, projection.Peers)
	}
	byPair := map[uint64]hub.Peer{}
	for _, peer := range projection.Peers {
		byPair[peer.PairID] = peer
	}
	if byPair[testPairID].ExpiresAt != uint64(later) {
		t.Fatalf("healthy pair was not republished: %+v", byPair[testPairID])
	}
	refused, ok := byPair[31]
	if !ok || refused.ExpiresAt != second.grant.validUntil || refused.PeerID != 21 {
		t.Fatalf("refused pair row changed: %+v", refused)
	}
	secondSummary, err := hub.LoadSummary(config.Pairs[1].SummaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if secondSummary.ConfigGen != 6 || secondSummary.PairGen != 1 {
		t.Fatalf("refused pair summary advanced: %+v", secondSummary)
	}
}

func TestRuntimeADPairPublishesLocalLoopback(t *testing.T) {
	key, public := testKey(t)
	config := coreConfig(t, "", public)
	config.Pairs[0].Mode = hub.PeerModeAD
	config.Pairs[0].LocalEndpoint = &hub.LocalEndpoint{Address: "127.0.0.1", Port: 14001}
	reply := baseReply(t, key, 1, 1, 1, 2, time.Now().Add(5*time.Minute).Unix())
	reply.binding.kind = 2
	reply.binding.address, reply.binding.port, reply.binding.family = "", 0, 0
	server := newFakeHub(t, func(_ *fakeHub, _ int, request recordedRequest) []byte {
		if request.u8(5) != 2 {
			t.Errorf("A-D request mode = %d", request.u8(5))
		}
		writeStatus(t, config.StatusPath, "RUNNING", 5, 1, "APPLIED", "POLICY_LOADED")
		return successPayload(t, key, reply)
	})
	config.HubAddress = server.listener.Addr().String()
	if err := stepOnce(t, newRuntime(t, config)); err != nil {
		t.Fatal(err)
	}
	projection, err := hub.Parse(readFile(t, filepath.Join(config.ConfDir, "utc.conf")))
	if err != nil {
		t.Fatal(err)
	}
	peer := projection.Peers[0]
	want := [16]byte{10: 0xff, 11: 0xff, 12: 127, 15: 1}
	if peer.Mode != hub.PeerModeAD || peer.Family != 4 || peer.Port != 14001 ||
		peer.Address != want || peer.ScopeID != 0 {
		t.Fatalf("A-D peer = %+v", peer)
	}
	verified, err := loadVerified(config.Pairs[0].VerifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Mode != hub.PeerModeAD || verified.PeerID != testPeerID {
		t.Fatalf("A-D verified = %+v", verified)
	}
}
