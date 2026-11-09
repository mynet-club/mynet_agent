package hub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type testGrant struct {
	grantID, issuer, keyID, scope, transport, edge, shard, reactor, account string
	pairID, pairGen, leaseEpoch, placementEpoch, quotaEpoch                 uint64
	byteLimit, issuedAt, notBefore, validUntil                              uint64
	side                                                                    uint8
	bindingDigest                                                           [32]byte
}

func encodeTestGrant(t *testing.T, key ed25519.PrivateKey, g testGrant) []byte {
	t.Helper()
	raw := make([]byte, grantBytes)
	copy(raw[:8], grantMagic[:])
	binary.BigEndian.PutUint16(raw[8:], 2)
	binary.BigEndian.PutUint16(raw[10:], 0)
	raw[grantSideOffset] = g.side
	binary.BigEndian.PutUint32(raw[grantTotalOffset:], grantBytes)
	tokens := []string{g.grantID, g.issuer, g.keyID, g.scope, g.transport,
		g.edge, g.shard, g.reactor, g.account, "slice-1"}
	for i, value := range tokens {
		if len(value) >= grantText {
			t.Fatalf("test token %q is too long", value)
		}
		copy(raw[grantTextOffset+i*grantText:], value)
	}
	binary.BigEndian.PutUint64(raw[grantPairIDOff:], g.pairID)
	binary.BigEndian.PutUint64(raw[grantPairGenOff:], g.pairGen)
	binary.BigEndian.PutUint64(raw[grantLeaseOff:], g.leaseEpoch)
	binary.BigEndian.PutUint64(raw[grantPlacementOff:], g.placementEpoch)
	binary.BigEndian.PutUint64(raw[grantQuotaEpoch:], g.quotaEpoch)
	binary.BigEndian.PutUint64(raw[grantByteLimitOff:], g.byteLimit)
	binary.BigEndian.PutUint64(raw[grantIssuedAtOff:], g.issuedAt)
	binary.BigEndian.PutUint64(raw[grantNotBeforeOff:], g.notBefore)
	binary.BigEndian.PutUint64(raw[grantValidOff:], g.validUntil)
	copy(raw[grantDigestOff:grantSignatureOff], g.bindingDigest[:])
	message := append(append([]byte{}, grantDomain...), raw[:grantUnsigned]...)
	copy(raw[grantSignatureOff:], ed25519.Sign(key, message))
	return raw
}

type testBinding struct {
	bindingID, issuer, keyID, scope, peerTransport, address string
	pairID, pairGen, placementEpoch                         uint64
	port                                                    uint16
	scopeID                                                 uint32
	kind, family, sourceSide, peerSide                      uint8
}

func encodeTestBinding(t *testing.T, key ed25519.PrivateKey, b testBinding) []byte {
	t.Helper()
	raw := make([]byte, bindingBytes)
	copy(raw[:8], bindingMagic[:])
	binary.BigEndian.PutUint16(raw[8:], 2)
	binary.BigEndian.PutUint16(raw[10:], 0)
	raw[bindingKindOffset] = b.kind
	raw[bindingFamilyOffset] = b.family
	raw[bindingSourceOffset] = b.sourceSide
	raw[bindingPeerOffset] = b.peerSide
	binary.BigEndian.PutUint32(raw[bindingTotalOffset:], bindingBytes)
	binary.BigEndian.PutUint16(raw[bindingPortOffset:], b.port)
	binary.BigEndian.PutUint32(raw[bindingScopeOff:], b.scopeID)
	tokens := []string{b.bindingID, b.issuer, b.keyID, b.scope, b.peerTransport}
	for i, value := range tokens {
		copy(raw[bindingTextOffset+i*grantText:], value)
	}
	copy(raw[bindingTextOffset+5*grantText:], b.address)
	binary.BigEndian.PutUint64(raw[bindingPairIDOff:], b.pairID)
	binary.BigEndian.PutUint64(raw[bindingPairGenOff:], b.pairGen)
	binary.BigEndian.PutUint64(raw[bindingPlacementOff:], b.placementEpoch)
	message := append(append([]byte{}, bindingDomain...), raw[:bindingUnsigned]...)
	copy(raw[bindingSignatureOff:], ed25519.Sign(key, message))
	return raw
}

type testReply struct {
	grant     testGrant
	binding   testBinding
	placement Placement
}

func successPayload(t *testing.T, key ed25519.PrivateKey, reply testReply) []byte {
	t.Helper()
	digest := sha256.Sum256(encodeTestBinding(t, key, reply.binding))
	reply.grant.bindingDigest = digest
	grant := encodeTestGrant(t, key, reply.grant)
	binding := encodeTestBinding(t, key, reply.binding)
	writer := newTLVWriter(2048)
	steps := []error{
		writer.addU16(controlReasonTLV, reasonOK),
		writer.add(controlGrantTLV, grant),
		writer.addU64(controlPlacementEpochTLV, reply.placement.Epoch),
		writer.addU32(controlEdgeTLV, reply.placement.EdgeID),
		writer.addU32(controlShardTLV, reply.placement.ShardID),
		writer.addU32(controlReactorTLV, reply.placement.ReactorID),
		writer.add(controlBindingTLV, binding),
	}
	for _, err := range steps {
		if err != nil {
			t.Fatal(err)
		}
	}
	return writer.buffer
}

type recordedRequest struct {
	fields map[uint16][]byte
}

func (r recordedRequest) u8(typ uint16) uint8 {
	value := r.fields[typ]
	if len(value) != 1 {
		return 0xff
	}
	return value[0]
}

func (r recordedRequest) u64(typ uint16) uint64 {
	value := r.fields[typ]
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}

func (r recordedRequest) text(typ uint16) string { return string(r.fields[typ]) }

type fakeHub struct {
	listener net.Listener
	mu       sync.Mutex
	requests []recordedRequest
}

// newFakeHub serves local TCP utc/2 CONTROL requests. Each client exchange is
// one connection and one request, matching utc-hub's sequential service loop.
func newFakeHub(t *testing.T, respond func(recordedRequest) []byte) *fakeHub {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hub := &fakeHub{listener: listener}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func(connection net.Conn) {
				defer connection.Close()
				frame, err := readControlFrame(connection)
				if err != nil {
					t.Errorf("fake hub read: %v", err)
					return
				}
				entries, err := parseTLVs(frame.Payload)
				if err != nil {
					t.Errorf("fake hub TLV: %v", err)
					return
				}
				request := recordedRequest{fields: map[uint16][]byte{}}
				for _, entry := range entries {
					request.fields[entry.Type] = entry.Value
				}
				hub.mu.Lock()
				hub.requests = append(hub.requests, request)
				hub.mu.Unlock()
				if respond == nil {
					return
				}
				payload := respond(request)
				if payload == nil {
					return
				}
				out, err := encodeControlFrame(frame.StreamID, payload)
				if err != nil {
					t.Errorf("fake hub encode: %v", err)
					return
				}
				if _, err := connection.Write(out); err != nil {
					t.Errorf("fake hub write: %v", err)
				}
			}(connection)
		}
	}()
	t.Cleanup(func() { listener.Close() })
	return hub
}

func (hub *fakeHub) count() int {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return len(hub.requests)
}

func (hub *fakeHub) last() recordedRequest {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.requests) == 0 {
		return recordedRequest{}
	}
	return hub.requests[len(hub.requests)-1]
}

func testKey(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey) {
	t.Helper()
	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	key := ed25519.NewKeyFromSeed(seed)
	return key, key.Public().(ed25519.PublicKey)
}

const testScope = "scope.test"
const testTransport = "transport-a"
const testAccount = "acct-a"
const testBoot = "abababababababababababababababab"

func testIdentity() Identity {
	return Identity{
		Scope: testScope, Instance: "instance-a", LocalID: 10, PeerID: 20,
		BootID: testBoot, PID: 123, Capabilities: CapabilityEpoch | CapabilityExplicitActivate,
		ConfigGen: 5,
	}
}

func testReplyFor(t *testing.T, key ed25519.PrivateKey, pairGen, lease, placementEpoch uint64,
	edge uint32, address string, port uint16) testReply {
	t.Helper()
	return testReply{
		grant: testGrant{
			grantID: "g1", issuer: "issuer", keyID: "key", scope: testScope,
			transport: testTransport, edge: strconv.FormatUint(uint64(edge), 10), shard: "0", reactor: "0",
			account: testAccount, pairID: 30, pairGen: pairGen, leaseEpoch: lease,
			placementEpoch: placementEpoch, quotaEpoch: 1, byteLimit: 1000000,
			issuedAt: 100, notBefore: 100, validUntil: 200, side: 0,
		},
		binding: testBinding{
			bindingID: "b-1", issuer: "issuer", keyID: "key", scope: testScope,
			peerTransport: "edge-2", address: address, pairID: 30, pairGen: pairGen,
			placementEpoch: placementEpoch, port: port, kind: bindingKindAS,
			family: 4, sourceSide: 0, peerSide: 1,
		},
		placement: Placement{Epoch: placementEpoch, EdgeID: edge, ShardID: 0, ReactorID: 0},
	}
}

func newTestSession(t *testing.T, hub *fakeHub, key ed25519.PrivateKey) *Session {
	t.Helper()
	client, err := NewClient(Config{
		Address: hub.listener.Addr().String(), Scope: testScope,
		TransportID: testTransport, AccountID: testAccount,
		IssuerPublicKey: key.Public().(ed25519.PublicKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Session{
		Client: client, Identity: testIdentity(),
		PairID: 30, Side: 0, Mode: controlModeAS,
	}
}

func TestAssignWritesUTCConf(t *testing.T) {
	key, _ := testKey(t)
	var reply testReply
	hub := newFakeHub(t, func(request recordedRequest) []byte {
		if request.u8(1) != 1 {
			t.Errorf("assign op = %d, want 1", request.u8(1))
		}
		if _, ok := request.fields[10]; ok {
			t.Error("assign must not carry pair_gen")
		}
		if request.text(2) != testScope || request.text(7) != testTransport || request.text(8) != testAccount {
			t.Error("assign identity fields do not match the configuration")
		}
		return successPayload(t, key, reply)
	})
	reply = testReplyFor(t, key, 1, 1, 1, 2, "203.0.113.7", 9443)
	session := newTestSession(t, hub, key)

	outcome, err := session.Assign(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Changed || !outcome.Response.Verified {
		t.Fatalf("assign outcome changed=%v verified=%v", outcome.Changed, outcome.Response.Verified)
	}
	if outcome.Summary.PairGen != 1 || outcome.Summary.LeaseEpoch != 1 ||
		outcome.Summary.PlacementEpoch != 1 || outcome.Summary.EdgeID != 2 ||
		outcome.Summary.BindingAddr != "203.0.113.7" || outcome.Summary.BindingPort != 9443 {
		t.Fatalf("summary did not carry the response values: %+v", outcome.Summary)
	}

	parsed, err := Parse(outcome.Text)
	if err != nil {
		t.Fatalf("generated utc.conf is not canonical: %v\n%s", err, outcome.Text)
	}
	peer := parsed.Peers[0]
	if peer.PairGen != 1 || peer.LeaseEpoch != 1 || peer.PlacementEpoch != 1 ||
		peer.Mode != 1 || peer.ExpiresAt != 200 || peer.Port != 9443 || peer.Family != 4 {
		t.Fatalf("utc.conf peer does not carry the response values: %+v", peer)
	}

	directory := t.TempDir()
	confPath := filepath.Join(directory, "utc.conf")
	summaryPath := filepath.Join(directory, "utc.hub.json")
	if err := outcome.Write(confPath, summaryPath); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, outcome.Text) {
		t.Fatal("written utc.conf differs from the canonical outcome")
	}
	info, err := os.Stat(confPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("utc.conf mode = %v", info.Mode())
	}
	rawSummary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	var summary Summary
	decoder := json.NewDecoder(bytes.NewReader(rawSummary))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&summary); err != nil {
		t.Fatal(err)
	}
	if summary.GrantDigest == "" || summary.BindingDigest != hex.EncodeToString(outcome.Response.Grant.TargetBindingDigest[:]) {
		t.Fatalf("summary digests are inconsistent: %+v", summary)
	}
	if !bytes.Contains(rawSummary, []byte(`"signature_verified":true`)) {
		t.Fatal("summary did not record signature verification")
	}
	loaded, err := LoadSummary(summaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Prior() != outcome.Prior() {
		t.Fatalf("persisted prior %+v does not match outcome %+v", loaded.Prior(), outcome.Prior())
	}
}

func TestRecoverRotatesOnBlockedStatus(t *testing.T) {
	key, _ := testKey(t)
	call := 0
	var first, second testReply
	hub := newFakeHub(t, func(request recordedRequest) []byte {
		call++
		if call == 1 {
			return successPayload(t, key, first)
		}
		if request.u8(1) != 2 || request.u8(12) != 1 {
			t.Errorf("recovery op=%d rotate=%d, want RENEW+rotate", request.u8(1), request.u8(12))
		}
		if request.u64(10) != 1 || request.u64(11) != 1 {
			t.Errorf("recovery presented pair_gen=%d lease_epoch=%d, want 1/1",
				request.u64(10), request.u64(11))
		}
		return successPayload(t, key, second)
	})
	first = testReplyFor(t, key, 1, 1, 1, 2, "203.0.113.7", 9443)
	second = testReplyFor(t, key, 2, 2, 1, 2, "203.0.113.7", 9443)
	session := newTestSession(t, hub, key)

	assigned, err := session.Assign(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	status := "status-v1 " + testScope + " instance-a 10 " + testBoot +
		" 5 1700000000 RECOVERED_BLOCKED 123\n" +
		"metrics 0 0 0 0\n" +
		"peer 20 30 1 BLOCKED REQUIRES_NEW_GENERATION " +
		strings.Repeat("cd", 32) + " " + strings.Repeat("ef", 64) + "\n"
	outcome, rotated, err := session.Recover(context.Background(), assigned.Prior(), []byte(status))
	if err != nil {
		t.Fatal(err)
	}
	if !rotated {
		t.Fatal("RECOVERED_BLOCKED did not request rotation")
	}
	if outcome.Summary.PairGen != 2 || outcome.Summary.LeaseEpoch != 2 ||
		outcome.Summary.RecoveryReason != "RECOVERED_BLOCKED" {
		t.Fatalf("rotation summary = %+v", outcome.Summary)
	}
	if !outcome.Changed || hub.count() != 2 {
		t.Fatalf("recovery changed=%v requests=%d", outcome.Changed, hub.count())
	}
}

func TestRecoverKeepsGenerationWhenApplied(t *testing.T) {
	key, _ := testKey(t)
	call := 0
	var reply testReply
	hub := newFakeHub(t, func(request recordedRequest) []byte {
		call++
		if call == 1 {
			return successPayload(t, key, reply)
		}
		if request.u8(1) != 2 {
			t.Errorf("renew op = %d, want 2", request.u8(1))
		}
		if _, ok := request.fields[12]; ok {
			t.Error("healthy pair must not rotate")
		}
		return successPayload(t, key, reply)
	})
	reply = testReplyFor(t, key, 4, 6, 3, 2, "203.0.113.7", 9443)
	// A direct Prior avoids a second assign; the first call below is the renew.
	session := newTestSession(t, hub, key)
	prior := Prior{PairGen: 4, LeaseEpoch: 6, Placement: Placement{Epoch: 3, EdgeID: 2}}
	call = 1 // make the first request the renew branch
	status := "status-v1 " + testScope + " instance-a 10 " + testBoot +
		" 5 1700000000 RUNNING 123\nmetrics 0 0 0 0\n" +
		"peer 20 30 4 APPLIED POLICY_LOADED " + strings.Repeat("cd", 32) + " " +
		strings.Repeat("ef", 64) + "\n"
	outcome, rotated, err := session.Recover(context.Background(), prior, []byte(status))
	if err != nil {
		t.Fatal(err)
	}
	if rotated {
		t.Fatal("healthy status requested rotation")
	}
	if outcome.Changed {
		t.Fatal("same-generation renewal must not republish the projection")
	}
	if outcome.Summary.PairGen != 4 || outcome.Summary.LeaseEpoch != 6 {
		t.Fatalf("renewal changed epochs: %+v", outcome.Summary)
	}
	directory := t.TempDir()
	confPath := filepath.Join(directory, "utc.conf")
	summaryPath := filepath.Join(directory, "utc.hub.json")
	if err := outcome.Write(confPath, summaryPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(confPath); !os.IsNotExist(err) {
		t.Fatal("same-generation renewal rewrote utc.conf")
	}
	if _, err := os.Stat(summaryPath); err != nil {
		t.Fatal("same-generation renewal did not persist its summary")
	}
}

func TestFailoverSendsFenceAndTakesNewPlacement(t *testing.T) {
	key, _ := testKey(t)
	var reply testReply
	hub := newFakeHub(t, func(request recordedRequest) []byte {
		if request.u8(1) != 2 || request.u8(6) != 1 || request.u8(12) != 1 {
			t.Errorf("failover op=%d failover=%d rotate=%d", request.u8(1), request.u8(6), request.u8(12))
		}
		if request.u64(10) != 3 || request.u64(11) != 7 {
			t.Errorf("failover epochs = %d/%d", request.u64(10), request.u64(11))
		}
		if request.u64(13) != 4 || binary.BigEndian.Uint32(request.fields[14]) != 2 ||
			binary.BigEndian.Uint32(request.fields[15]) != 0 {
			t.Error("failover did not name the old placement")
		}
		if request.u64(16) != 9 || request.u64(17) != 11 || request.u64(18) != 12 {
			t.Error("fence id/gate/journal fields do not match")
		}
		if !bytes.Equal(request.fields[19], bytes.Repeat([]byte{0x01}, 16)) ||
			!bytes.Equal(request.fields[20], bytes.Repeat([]byte{0x02}, 32)) ||
			!bytes.Equal(request.fields[21], bytes.Repeat([]byte{0x03}, 32)) {
			t.Error("fence evidence digests do not match")
		}
		return successPayload(t, key, reply)
	})
	// The replacement changes Edge, pair_gen/lease_epoch and placement epoch.
	reply = testReplyFor(t, key, 4, 8, 5, 3, "203.0.113.9", 9555)
	session := newTestSession(t, hub, key)
	prior := Prior{PairGen: 3, LeaseEpoch: 7, Placement: Placement{Epoch: 4, EdgeID: 2}}
	fence := Fence{
		ID: 9, GateEpoch: 11, JournalLSN: 12,
		BootNonce: [16]byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		Receipts:  [32]byte{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2},
		Meter:     [32]byte{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3},
	}
	outcome, err := session.Failover(context.Background(), prior, fence)
	if err != nil {
		t.Fatal(err)
	}
	peer := outcome.Projection.Peers[0]
	if peer.PairGen != 4 || peer.LeaseEpoch != 8 || peer.PlacementEpoch != 5 ||
		peer.Port != 9555 || peer.Family != 4 {
		t.Fatalf("failover projection did not take the new response: %+v", peer)
	}
	if outcome.Summary.EdgeID != 3 || !outcome.Changed {
		t.Fatalf("failover summary = %+v changed=%v", outcome.Summary, outcome.Changed)
	}
}

func TestRejectsTamperedBinding(t *testing.T) {
	key, _ := testKey(t)
	var reply testReply
	hub := newFakeHub(t, func(recordedRequest) []byte {
		payload := successPayload(t, key, reply)
		entries, err := parseTLVs(payload)
		if err != nil {
			t.Fatal(err)
		}
		binding := entries[6].Value
		binding[bindingTextOffset+5*grantText] = '9' // address byte after the digest was signed
		return payload
	})
	reply = testReplyFor(t, key, 1, 1, 1, 2, "203.0.113.7", 9443)
	session := newTestSession(t, hub, key)
	if _, err := session.Assign(context.Background()); err == nil {
		t.Fatal("accepted a binding that does not match the grant digest")
	}
}

func TestRejectsForeignSignature(t *testing.T) {
	key, _ := testKey(t)
	foreign := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x24}, ed25519.SeedSize))
	var reply testReply
	hub := newFakeHub(t, func(recordedRequest) []byte { return successPayload(t, foreign, reply) })
	reply = testReplyFor(t, foreign, 1, 1, 1, 2, "203.0.113.7", 9443)
	session := newTestSession(t, hub, key)
	_, err := session.Assign(context.Background())
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("accepted a foreign-signed grant: %v", err)
	}
}

func TestFailoverRequiresFenceEvidenceBeforeDial(t *testing.T) {
	key, _ := testKey(t)
	hub := newFakeHub(t, func(recordedRequest) []byte { return nil })
	session := newTestSession(t, hub, key)
	prior := Prior{PairGen: 1, LeaseEpoch: 1, Placement: Placement{Epoch: 1, EdgeID: 2}}
	_, err := session.Failover(context.Background(), prior, Fence{ID: 1, GateEpoch: 1, JournalLSN: 1})
	if err == nil {
		t.Fatal("accepted failover without fence digests")
	}
	if hub.count() != 0 {
		t.Fatal("invalid failover reached the network")
	}
}

func TestRefusalReasonIsTyped(t *testing.T) {
	key, _ := testKey(t)
	hub := newFakeHub(t, func(recordedRequest) []byte {
		writer := newTLVWriter(16)
		if err := writer.addU16(controlReasonTLV, reasonFenceRequired); err != nil {
			t.Fatal(err)
		}
		return writer.buffer
	})
	session := newTestSession(t, hub, key)
	_, err := session.Assign(context.Background())
	var reason *ReasonError
	if !errors.As(err, &reason) || reason.Reason != reasonFenceRequired {
		t.Fatalf("refusal error = %v, want FENCE_REQUIRED", err)
	}
}

func TestRotationRequired(t *testing.T) {
	header := "status-v1 " + testScope + " instance-a 10 " + testBoot + " 5 1700000000 %s 123\nmetrics 0 0 0 0\n"
	peer := "peer 20 30 4 %s %s " + strings.Repeat("cd", 32) + " " + strings.Repeat("ef", 64) + "\n"
	cases := []struct {
		status  string
		rotate  bool
		reason  string
		wantErr bool
	}{
		{header + "", false, "", false},
		{withHeaderReason(header, "RECOVERED_BLOCKED"), true, "RECOVERED_BLOCKED", false},
		{withHeaderReason(header, "RUNNING") + withPeerState(peer, "APPLIED", "POLICY_LOADED"), false, "POLICY_LOADED", false},
		{withHeaderReason(header, "RUNNING") + withPeerState(peer, "BLOCKED", "EXPIRED"), true, "EXPIRED", false},
		{withHeaderReason(header, "RUNNING") + withPeerState(peer, "BLOCKED", "REQUIRES_NEW_GENERATION"), true, "REQUIRES_NEW_GENERATION", false},
		{"garbage", false, "", true},
	}
	for _, test := range cases {
		rotate, reason, err := RotationRequired([]byte(test.status), 20, 30)
		if test.wantErr != (err != nil) {
			t.Fatalf("status %q error = %v", test.status, err)
		}
		if err == nil && (rotate != test.rotate || reason != test.reason) {
			t.Fatalf("status %q rotate=%v reason=%q, want %v/%q",
				test.status, rotate, reason, test.rotate, test.reason)
		}
	}
}

func withHeaderReason(format, reason string) string {
	return strings.Replace(format, "%s", reason, 1)
}

func withPeerState(format, state, reason string) string {
	return strings.Replace(strings.Replace(format, "%s", state, 1), "%s", reason, 1)
}

func TestClientRequiresIssuerKey(t *testing.T) {
	if _, err := NewClient(Config{
		Address: "127.0.0.1:1", Scope: testScope,
		TransportID: testTransport, AccountID: testAccount,
	}); err == nil {
		t.Fatal("accepted a client without an issuer trust anchor")
	}
}

func TestRejectsIncompleteSuccessReply(t *testing.T) {
	key, _ := testKey(t)
	hub := newFakeHub(t, func(recordedRequest) []byte {
		writer := newTLVWriter(8)
		if err := writer.addU16(controlReasonTLV, reasonOK); err != nil {
			t.Fatal(err)
		}
		return writer.buffer
	})
	session := newTestSession(t, hub, key)
	if _, err := session.Assign(context.Background()); err == nil {
		t.Fatal("accepted a success reply without grant/binding/placement")
	}
}
