package hubd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Independent utc/2 fixture encoder. The runtime tests must build replies the
// same way the frozen hub does, not by reusing hub-internal test helpers.

const (
	tGrantBytes    = 826
	tGrantUnsigned = 762
	tGrantText     = 64
	tGrantSide     = 12
	tGrantTotal    = 14
	tGrantTextOff  = 18
	tGrantPairID   = 658
	tGrantPairGen  = 666
	tGrantLease    = 674
	tGrantPlace    = 682
	tGrantQuota    = 690
	tGrantBytesF   = 698
	tGrantIssued   = 706
	tGrantNotBef   = 714
	tGrantValid    = 722
	tGrantDigest   = 730
	tGrantSig      = 762

	tBindingBytes    = 500
	tBindingUnsigned = 436
	tBindingKind     = 12
	tBindingFamily   = 13
	tBindingSource   = 14
	tBindingPeer     = 15
	tBindingTotal    = 16
	tBindingPort     = 20
	tBindingScope    = 22
	tBindingReserve  = 26
	tBindingTextOff  = 28
	tBindingPairID   = 412
	tBindingPairGen  = 420
	tBindingPlace    = 428
	tBindingSig      = 436

	tFrameVersion = 2
	tFrameType    = 1
	tFrameHeader  = 12
	tFrameMax     = 65535
	tTLVHeader    = 6

	tReasonTLV    = 1
	tGrantTLV     = 2
	tPlacementTLV = 3
	tEdgeTLV      = 4
	tShardTLV     = 5
	tReactorTLV   = 6
	tBindingTLV   = 7
	tReasonOK     = 0
	tKindAS       = 1
)

var (
	tGrantMagic    = []byte("UTCHGRT2")
	tBindingMagic  = []byte("UTCHBND2")
	tGrantDomain   = []byte("mynet.utc.hub.data-grant.v2")
	tBindingDomain = []byte("mynet.utc.hub.target-binding.v2")
)

type testGrant struct {
	grantID, issuer, keyID, scope, transport, edge, shard, reactor, account string
	pairID, pairGen, leaseEpoch, placementEpoch, quotaEpoch                 uint64
	byteLimit, issuedAt, notBefore, validUntil                              uint64
	side                                                                    uint8
	bindingDigest                                                           [32]byte
}

type testBinding struct {
	bindingID, issuer, keyID, scope, peerTransport, address string
	pairID, pairGen, placementEpoch                         uint64
	port                                                    uint16
	scopeID                                                 uint32
	kind, family, sourceSide, peerSide                      uint8
}

type testReply struct {
	grant     testGrant
	binding   testBinding
	placement Placement
}

type Placement struct {
	Epoch, EdgeID, ShardID, ReactorID uint64
}

func encodeTestGrant(t *testing.T, key ed25519.PrivateKey, g testGrant) []byte {
	t.Helper()
	raw := make([]byte, tGrantBytes)
	copy(raw[:8], tGrantMagic)
	binary.BigEndian.PutUint16(raw[8:], 2)
	raw[tGrantSide] = g.side
	binary.BigEndian.PutUint32(raw[tGrantTotal:], tGrantBytes)
	tokens := []string{g.grantID, g.issuer, g.keyID, g.scope, g.transport,
		g.edge, g.shard, g.reactor, g.account, "slice-1"}
	for i, value := range tokens {
		if len(value) >= tGrantText {
			t.Fatalf("test token %q is too long", value)
		}
		copy(raw[tGrantTextOff+i*tGrantText:], value)
	}
	binary.BigEndian.PutUint64(raw[tGrantPairID:], g.pairID)
	binary.BigEndian.PutUint64(raw[tGrantPairGen:], g.pairGen)
	binary.BigEndian.PutUint64(raw[tGrantLease:], g.leaseEpoch)
	binary.BigEndian.PutUint64(raw[tGrantPlace:], g.placementEpoch)
	binary.BigEndian.PutUint64(raw[tGrantQuota:], g.quotaEpoch)
	binary.BigEndian.PutUint64(raw[tGrantBytesF:], g.byteLimit)
	binary.BigEndian.PutUint64(raw[tGrantIssued:], g.issuedAt)
	binary.BigEndian.PutUint64(raw[tGrantNotBef:], g.notBefore)
	binary.BigEndian.PutUint64(raw[tGrantValid:], g.validUntil)
	copy(raw[tGrantDigest:tGrantSig], g.bindingDigest[:])
	message := append(append([]byte{}, tGrantDomain...), raw[:tGrantUnsigned]...)
	copy(raw[tGrantSig:], ed25519.Sign(key, message))
	return raw
}

func encodeTestBinding(t *testing.T, key ed25519.PrivateKey, b testBinding) []byte {
	t.Helper()
	raw := make([]byte, tBindingBytes)
	copy(raw[:8], tBindingMagic)
	binary.BigEndian.PutUint16(raw[8:], 2)
	raw[tBindingKind] = b.kind
	raw[tBindingFamily] = b.family
	raw[tBindingSource] = b.sourceSide
	raw[tBindingPeer] = b.peerSide
	binary.BigEndian.PutUint32(raw[tBindingTotal:], tBindingBytes)
	binary.BigEndian.PutUint16(raw[tBindingPort:], b.port)
	binary.BigEndian.PutUint32(raw[tBindingScope:], b.scopeID)
	tokens := []string{b.bindingID, b.issuer, b.keyID, b.scope, b.peerTransport}
	for i, value := range tokens {
		copy(raw[tBindingTextOff+i*tGrantText:], value)
	}
	copy(raw[tBindingTextOff+5*tGrantText:], b.address)
	binary.BigEndian.PutUint64(raw[tBindingPairID:], b.pairID)
	binary.BigEndian.PutUint64(raw[tBindingPairGen:], b.pairGen)
	binary.BigEndian.PutUint64(raw[tBindingPlace:], b.placementEpoch)
	message := append(append([]byte{}, tBindingDomain...), raw[:tBindingUnsigned]...)
	copy(raw[tBindingSig:], ed25519.Sign(key, message))
	return raw
}

func successPayload(t *testing.T, key ed25519.PrivateKey, reply testReply) []byte {
	t.Helper()
	digest := sha256.Sum256(encodeTestBinding(t, key, reply.binding))
	reply.grant.bindingDigest = digest
	grant := encodeTestGrant(t, key, reply.grant)
	binding := encodeTestBinding(t, key, reply.binding)
	writer := newTestTLVWriter()
	var steps []error
	steps = append(steps, writer.addU16(tReasonTLV, tReasonOK))
	steps = append(steps, writer.add(tGrantTLV, grant))
	steps = append(steps, writer.addU64(tPlacementTLV, reply.placement.Epoch))
	steps = append(steps, writer.addU32(tEdgeTLV, uint32(reply.placement.EdgeID)))
	steps = append(steps, writer.addU32(tShardTLV, uint32(reply.placement.ShardID)))
	steps = append(steps, writer.addU32(tReactorTLV, uint32(reply.placement.ReactorID)))
	steps = append(steps, writer.add(tBindingTLV, binding))
	for _, err := range steps {
		if err != nil {
			t.Fatal(err)
		}
	}
	return writer.buffer
}

type testTLVWriter struct {
	buffer   []byte
	previous uint16
	count    int
}

func newTestTLVWriter() *testTLVWriter { return &testTLVWriter{buffer: make([]byte, 0, 2048)} }

func (w *testTLVWriter) add(typ uint16, value []byte) error {
	if typ == 0 || w.count > 0 && typ <= w.previous {
		return fmt.Errorf("bad tlv type %d", typ)
	}
	var header [tTLVHeader]byte
	binary.BigEndian.PutUint16(header[:2], typ)
	binary.BigEndian.PutUint32(header[2:], uint32(len(value)))
	w.buffer = append(w.buffer, header[:]...)
	w.buffer = append(w.buffer, value...)
	w.previous = typ
	w.count++
	return nil
}

func (w *testTLVWriter) addU16(typ uint16, value uint16) error {
	var raw [2]byte
	binary.BigEndian.PutUint16(raw[:], value)
	return w.add(typ, raw[:])
}

func (w *testTLVWriter) addU32(typ uint16, value uint32) error {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], value)
	return w.add(typ, raw[:])
}

func (w *testTLVWriter) addU64(typ uint16, value uint64) error {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], value)
	return w.add(typ, raw[:])
}

type recordedRequest struct{ fields map[uint16][]byte }

func (r recordedRequest) u8(typ uint16) uint8 {
	if len(r.fields[typ]) != 1 {
		return 0xff
	}
	return r.fields[typ][0]
}

func (r recordedRequest) u64(typ uint16) uint64 {
	if len(r.fields[typ]) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(r.fields[typ])
}

func (r recordedRequest) text(typ uint16) string { return string(r.fields[typ]) }

type fakeHub struct {
	listener net.Listener
	mu       sync.Mutex
	requests []recordedRequest
}

// newFakeHub serves one utc/2 CONTROL request per connection. Returning nil
// from respond closes the connection without a reply, which the client treats
// as a transport failure.
func newFakeHub(t *testing.T, respond func(*fakeHub, int, recordedRequest) []byte) *fakeHub {
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
				frame, err := readTestFrame(connection)
				if err != nil {
					return
				}
				request := recordedRequest{fields: map[uint16][]byte{}}
				offset := 0
				for offset < len(frame.Payload) {
					if len(frame.Payload)-offset < tTLVHeader {
						return
					}
					typ := binary.BigEndian.Uint16(frame.Payload[offset:])
					length := binary.BigEndian.Uint32(frame.Payload[offset+2:])
					offset += tTLVHeader
					if uint64(length) > uint64(len(frame.Payload)-offset) {
						return
					}
					request.fields[typ&0x7fff] = frame.Payload[offset : offset+int(length)]
					offset += int(length)
				}
				hub.mu.Lock()
				hub.requests = append(hub.requests, request)
				index := len(hub.requests) - 1
				hub.mu.Unlock()
				payload := respond(hub, index, request)
				if payload == nil {
					return
				}
				reply, err := encodeTestFrame(frame.StreamID, payload)
				if err != nil {
					return
				}
				_, _ = connection.Write(reply)
			}(connection)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return hub
}

func (hub *fakeHub) count() int {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return len(hub.requests)
}

func (hub *fakeHub) at(index int) recordedRequest {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if index < 0 || index >= len(hub.requests) {
		return recordedRequest{}
	}
	return hub.requests[index]
}

func readTestFrame(reader io.Reader) (testFrame, error) {
	var frame testFrame
	var header [tFrameHeader]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return frame, err
	}
	if header[0] != tFrameVersion || header[1] != tFrameType {
		return frame, fmt.Errorf("bad frame header")
	}
	length := binary.BigEndian.Uint32(header[8:])
	if length > tFrameMax {
		return frame, fmt.Errorf("oversized frame")
	}
	frame.StreamID = binary.BigEndian.Uint32(header[4:])
	frame.Payload = make([]byte, length)
	_, err := io.ReadFull(reader, frame.Payload)
	return frame, err
}

type testFrame struct {
	StreamID uint32
	Payload  []byte
}

func encodeTestFrame(streamID uint32, payload []byte) ([]byte, error) {
	if len(payload) > tFrameMax {
		return nil, fmt.Errorf("oversized frame")
	}
	out := make([]byte, tFrameHeader+len(payload))
	out[0], out[1] = tFrameVersion, tFrameType
	binary.BigEndian.PutUint32(out[4:], streamID)
	binary.BigEndian.PutUint32(out[8:], uint32(len(payload)))
	copy(out[tFrameHeader:], payload)
	return out, nil
}

const (
	testScope     = "scope.test"
	testTransport = "transport-a"
	testAccount   = "acct-a"
	testBoot      = "abababababababababababababababab"
	testPairID    = 30
	testPeerID    = 20
	testLocalID   = 10
)

func testKey(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	return key, key.Public().(ed25519.PublicKey)
}

func testReplyFor(t *testing.T, key ed25519.PrivateKey, pairGen, lease, placement uint64,
	edge uint64, address string, port uint16, issuedAt, validUntil uint64) testReply {
	t.Helper()
	return testReply{
		grant: testGrant{
			grantID: "g1", issuer: "issuer", keyID: "key", scope: testScope,
			transport: testTransport, edge: strconv.FormatUint(edge, 10), shard: "0", reactor: "0",
			account: testAccount, pairID: testPairID, pairGen: pairGen, leaseEpoch: lease,
			placementEpoch: placement, quotaEpoch: 1, byteLimit: 1000000,
			issuedAt: issuedAt, notBefore: issuedAt, validUntil: validUntil, side: 0,
		},
		binding: testBinding{
			bindingID: "b-1", issuer: "issuer", keyID: "key", scope: testScope,
			peerTransport: "edge-2", address: address, pairID: testPairID, pairGen: pairGen,
			placementEpoch: placement, port: port, kind: tKindAS,
			family: 4, sourceSide: 0, peerSide: 1,
		},
		placement: Placement{Epoch: placement, EdgeID: edge},
	}
}

const testManifest = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
const testLocalDigest = "efefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefef"

func writeStatus(t *testing.T, path, headerReason string, configGen, pairGen uint64, state, reason string) {
	t.Helper()
	writeStatusFor(t, path, headerReason, configGen, pairGen, state, reason, testBoot, 123)
}

func writeStatusFor(t *testing.T, path, headerReason string, configGen, pairGen uint64,
	state, reason, bootID string, pid uint64) {
	t.Helper()
	now := uint64(time.Now().Unix())
	text := fmt.Sprintf("status-v1 %s instance-a %d %s %d %d %s %d\n"+
		"metrics 0 0 0 0\n"+
		"peer %d %d %d %s %s %s %s\n",
		testScope, testLocalID, bootID, configGen, now, headerReason, pid,
		testPeerID, testPairID, pairGen, state, reason, testManifest, testLocalDigest)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func statusText(headerReason string, configGen, pairGen uint64, state, reason string) string {
	return fmt.Sprintf("status-v1 %s instance-a %d %s %d %d %s %d\n"+
		"metrics 0 0 0 0\n"+
		"peer %d %d %d %s %s %s %s\n",
		testScope, testLocalID, testBoot, configGen, uint64(time.Now().Unix()),
		headerReason, 123, testPeerID, testPairID, pairGen, state, reason,
		testManifest, testLocalDigest)
}
