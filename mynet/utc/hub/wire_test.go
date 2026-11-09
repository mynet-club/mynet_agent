package hub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"net"
	"strings"
	"testing"
)

// canonicalAssignHex is produced by the C encoder in
// mynet_utc/tests/test_hub_wire.c (test_control_golden). Both language
// implementations must agree on these bytes.
const canonicalAssignHex = "02010000000000070000005e0001000000010100020000000a73636f70652e746573740003" +
	"00000008000000000000001e000400000001000005000000010100070000000b7472616e7370" +
	"6f72742d61000800000006616363742d61000900000008112210f47de98115"

func testClient(t *testing.T, address string, key ed25519.PublicKey) *Client {
	t.Helper()
	client, err := NewClient(Config{
		Address: address, Scope: testScope, TransportID: testTransport,
		AccountID: testAccount, IssuerPublicKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestControlFrameMatchesSharedGolden(t *testing.T) {
	_, publicKey := testKey(t)
	client := testClient(t, "127.0.0.1:1", publicKey)
	payload, err := client.encodeRequest(Request{
		PairID: 30, Side: 0, Mode: controlModeAS, Nonce: 1234567890123456789,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeControlFrame(7, payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(raw); got != canonicalAssignHex {
		t.Fatalf("control frame drifted from the shared golden:\n got %s\nwant %s",
			got, canonicalAssignHex)
	}
}

func TestReadControlFrameRejectsMalformed(t *testing.T) {
	valid, err := encodeControlFrame(7, []byte{0, 1, 0, 0, 0, 1, 0x01})
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(f func([]byte)) []byte {
		raw := append([]byte{}, valid...)
		f(raw)
		return raw
	}
	cases := []struct {
		name string
		raw  []byte
	}{
		{"truncated header", valid[:5]},
		{"unsupported version", mutate(func(raw []byte) { raw[0] = 1 })},
		{"non-control type", mutate(func(raw []byte) { raw[1] = 4 })},
		{"unsupported flags", mutate(func(raw []byte) { raw[3] = 1 })},
		{"oversize payload", mutate(func(raw []byte) {
			binary.BigEndian.PutUint32(raw[8:], framePayloadMax+1)
		})},
		{"truncated payload", func() []byte {
			raw := append([]byte{}, valid...)
			binary.BigEndian.PutUint32(raw[8:], 9)
			return raw[:len(raw)-1]
		}()},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := readControlFrame(bytes.NewReader(test.raw)); err == nil {
				t.Fatal("malformed frame was accepted")
			}
		})
	}
}

func TestParseTLVsRejectsMalformed(t *testing.T) {
	header := func(typ uint16, length uint32) []byte {
		raw := make([]byte, 6)
		binary.BigEndian.PutUint16(raw, typ)
		binary.BigEndian.PutUint32(raw[2:], length)
		return raw
	}
	ordered := append(append(header(4, 0), header(5, 0)...), header(7, 1)...)
	ordered = append(ordered, 0x00)
	cases := []struct {
		name string
		raw  []byte
	}{
		{"zero type", header(0, 0)},
		{"critical unknown", header(0x8001, 0)},
		{"descending type", append(append(header(5, 0), header(4, 0)...), 0)},
		{"duplicate type", append(append(header(5, 0), header(5, 0)...), 0)},
		{"truncated header", header(1, 0)[:4]},
		{"truncated value", append(header(1, 4), 0x00)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseTLVs(test.raw); err == nil {
				t.Fatal("malformed TLV stream was accepted")
			}
		})
	}
	if entries, err := parseTLVs(ordered); err != nil || len(entries) != 3 {
		t.Fatalf("valid TLV stream rejected: entries=%d err=%v", len(entries), err)
	}
}

func TestDecodeResponseRejectsMalformedPayloads(t *testing.T) {
	key, publicKey := testKey(t)
	client := testClient(t, "127.0.0.1:1", publicKey)
	request := Request{PairID: 30, Side: 0, Mode: controlModeAS, Nonce: 1}
	reply := testReplyFor(t, key, 1, 1, 1, 1, "10.0.0.1", 19620)
	valid := successPayload(t, key, reply)

	truncatedGrant := func(t *testing.T) []byte {
		entries, err := parseTLVs(valid)
		if err != nil {
			t.Fatal(err)
		}
		writer := newTLVWriter(2048)
		for _, entry := range entries {
			value := entry.Value
			if entry.Type == controlGrantTLV {
				value = value[:len(value)-1]
			}
			if err := writer.add(entry.Type, value); err != nil {
				t.Fatal(err)
			}
		}
		return writer.buffer
	}
	digestMismatch := func(t *testing.T) []byte {
		binding := encodeTestBinding(t, key, reply.binding)
		reply.grant.bindingDigest = [32]byte{}
		grant := encodeTestGrant(t, key, reply.grant)
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
	refusalWithPayload := func(t *testing.T) []byte {
		writer := newTLVWriter(64)
		if err := writer.addU16(controlReasonTLV, reasonFenceRequired); err != nil {
			t.Fatal(err)
		}
		if err := writer.addU8(controlPlacementEpochTLV, 1); err != nil {
			t.Fatal(err)
		}
		return writer.buffer
	}
	cases := []struct {
		name string
		raw  []byte
	}{
		{"empty payload", nil},
		{"reason missing", []byte{0, 2, 0, 0, 0, 2}},
		{"refusal with payload", refusalWithPayload(t)},
		{"truncated grant", truncatedGrant(t)},
		{"binding digest mismatch", digestMismatch(t)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.decodeResponse(test.raw, request); err == nil {
				t.Fatal("malformed control reply was accepted")
			}
		})
	}
}

func TestRejectsWrongReplyStream(t *testing.T) {
	key, publicKey := testKey(t)
	payload := successPayload(t, key, testReplyFor(t, key, 1, 1, 1, 1, "10.0.0.1", 19620))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
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
					return
				}
				out, err := encodeControlFrame(frame.StreamID+1, payload)
				if err != nil {
					return
				}
				_, _ = connection.Write(out)
			}(connection)
		}
	}()
	session := &Session{
		Client:   testClient(t, listener.Addr().String(), publicKey),
		Identity: testIdentity(), PairID: 30, Side: 0, Mode: controlModeAS,
	}
	_, err = session.Assign(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stream") {
		t.Fatalf("wrong reply stream was accepted: %v", err)
	}
}
