package nodedesc

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"math"
	"testing"
)

// Golden vector shared with the C implementation: a real Ed25519 signature by
// the key below over a 452-byte NodeDescriptor.
const (
	goldenFrameHex  = "5554434e4f44533100010000000001c424e30000636f72652e7072696d6172792d3100000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000007574632d636f72652e31000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000636f72652d6b65792d32303236610000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000636f7265000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000636f72652e6578616d706c652e6e6574000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000006553f164000000006553ff74505152535455565758595a5b5c5d5e5f606162636465666768696a6b6c6d6e6f867bf2f73d046e744e2ac9a0f30663414117404ebe827e3ed840a9bc7ce7f08bc2cd1a90db61e8e9e09ac836c77d88369012fcd1e508485f7b749181efd2660b"
	goldenPublicHex = "79b5562e8fe654f94078b112e8a98ba7901f853ae695bed7e0e3910bad049664"

	goldenNotBefore  = 1700000100
	goldenValidUntil = 1700003700
)

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}
	return decoded
}

func goldenFrame(t *testing.T) []byte {
	t.Helper()
	frame := mustHex(t, goldenFrameHex)
	if len(frame) != frameBytes {
		t.Fatalf("golden frame length = %d, want %d", len(frame), frameBytes)
	}
	return frame
}

func goldenPublicKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	return ed25519.PublicKey(mustHex(t, goldenPublicHex))
}

func goldenPin() [32]byte {
	var pin [32]byte
	for i := range pin {
		pin[i] = byte(0x50 + i)
	}
	return pin
}

func goldenExpect() Expect {
	return Expect{
		IssuerID:  "utc-core.1",
		KeyID:     "core-key-2026a",
		Scope:     "core",
		Pin:       goldenPin(),
		NowMS:     1700000200,
		MaxSkewMS: 100,
	}
}

func goldenDescriptor() Descriptor {
	return Descriptor{
		CoreID:       "core.primary-1",
		IssuerID:     "utc-core.1",
		KeyID:        "core-key-2026a",
		Scope:        "core",
		EndpointHost: "core.example.net",
		Port:         9443,
		NotBefore:    goldenNotBefore,
		ValidUntil:   goldenValidUntil,
		SPKIPin:      goldenPin(),
	}
}

// TestGoldenDecodeAndVerify anchors the Go decoder/verifier to the frozen C
// frame and real Ed25519 signature.
func TestGoldenDecodeAndVerify(t *testing.T) {
	frame := goldenFrame(t)
	want := goldenDescriptor()

	decoded, result := Decode(frame)
	if result != OK {
		t.Fatalf("Decode result = %v, want OK", result)
	}
	if decoded != want {
		t.Fatalf("Decode descriptor = %+v, want %+v", decoded, want)
	}

	verified, result := Verify(frame, goldenExpect(), goldenPublicKey(t))
	if result != OK {
		t.Fatalf("Verify result = %v, want OK", result)
	}
	if verified != want {
		t.Fatalf("Verify descriptor = %+v, want %+v", verified, want)
	}
}

// TestTamperedBodyIsSignatureFailure changes a single body or signature byte.
// The frame stays structurally valid, so the failure must be Signature rather
// than Format.
func TestTamperedBodyIsSignatureFailure(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(frame []byte)
	}{
		{"core_id", func(f []byte) { f[offCoreID+3] ^= 0x01 }},
		{"issuer_id", func(f []byte) { f[offIssuerID] ^= 0x01 }},
		{"key_id", func(f []byte) { f[offKeyID] ^= 0x01 }},
		{"scope", func(f []byte) { f[offScope] ^= 0x01 }},
		{"endpoint_host", func(f []byte) { f[offEndpoint] ^= 0x01 }},
		{"not_before", func(f []byte) { f[offNotBefore+7] ^= 0x01 }},
		{"valid_until", func(f []byte) { f[offValidUntil+7] ^= 0x01 }},
		{"spki_pin", func(f []byte) { f[offPin] ^= 0x01 }},
		{"signature", func(f []byte) { f[offSignature] ^= 0x01 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := goldenFrame(t)
			tc.mutate(frame)
			if _, result := Decode(frame); result != OK {
				t.Fatalf("Decode after tamper = %v, want OK (structure must stay valid)", result)
			}
			if _, result := Verify(frame, goldenExpect(), goldenPublicKey(t)); result != Signature {
				t.Fatalf("Verify after tamper = %v, want Signature", result)
			}
		})
	}
}

func TestWrongPublicKeyIsSignature(t *testing.T) {
	_, otherPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	otherPublic := otherPrivate.Public().(ed25519.PublicKey)

	cases := []struct {
		name string
		key  ed25519.PublicKey
	}{
		{"other_key", otherPublic},
		{"nil", nil},
		{"short", ed25519.PublicKey(make([]byte, ed25519.PublicKeySize-1))},
		{"long", ed25519.PublicKey(make([]byte, ed25519.PublicKeySize+1))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, result := Verify(goldenFrame(t), goldenExpect(), tc.key); result != Signature {
				t.Fatalf("Verify result = %v, want Signature", result)
			}
		})
	}
}

func TestBindingAndPinMismatch(t *testing.T) {
	wrongIssuer := goldenExpect()
	wrongIssuer.IssuerID = "utc-core.2"
	wrongKey := goldenExpect()
	wrongKey.KeyID = "core-key-2026b"
	wrongScope := goldenExpect()
	wrongScope.Scope = "Core" // binding is exact, case-sensitive
	wrongPin := goldenExpect()
	wrongPin.Pin[31] ^= 0x01

	cases := []struct {
		name   string
		expect Expect
		want   Result
	}{
		{"issuer", wrongIssuer, Binding},
		{"key", wrongKey, Binding},
		{"scope_case", wrongScope, Binding},
		{"pin", wrongPin, Pin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, result := Verify(goldenFrame(t), tc.expect, goldenPublicKey(t)); result != tc.want {
				t.Fatalf("Verify result = %v, want %v", result, tc.want)
			}
		})
	}

	// Binding failures are detected before the pin check.
	if _, result := Verify(goldenFrame(t), wrongIssuer, goldenPublicKey(t)); result != Binding {
		t.Fatalf("issuer mismatch with correct pin = %v, want Binding", result)
	}
	// A wrong pin is detected before the rollback floor.
	rolled := wrongPin
	rolled.MinValidUntil = goldenValidUntil + 1
	if _, result := Verify(goldenFrame(t), rolled, goldenPublicKey(t)); result != Pin {
		t.Fatalf("pin mismatch with rollback floor = %v, want Pin", result)
	}
}

func TestTimeWindowBoundaries(t *testing.T) {
	cases := []struct {
		name string
		now  uint64
		skew uint64
		want Result
	}{
		{"not_before_future_beyond_skew", goldenNotBefore - 100 - 1, 100, NotYetValid},
		{"not_before_future_at_skew", goldenNotBefore - 100, 100, OK},
		{"not_before_exactly_now", goldenNotBefore, 100, OK},
		{"valid_until_minus_one", goldenValidUntil - 1, 100, OK},
		{"valid_until_plus_skew_minus_one", goldenValidUntil + 100 - 1, 100, OK},
		{"valid_until_plus_skew", goldenValidUntil + 100, 100, Expired},
		{"valid_until_exactly_now_no_skew", goldenValidUntil, 0, Expired},
		{"valid_until_minus_one_no_skew", goldenValidUntil - 1, 0, OK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect := goldenExpect()
			expect.NowMS = tc.now
			expect.MaxSkewMS = tc.skew
			if _, result := Verify(goldenFrame(t), expect, goldenPublicKey(t)); result != tc.want {
				t.Fatalf("Verify(now=%d, skew=%d) = %v, want %v", tc.now, tc.skew, result, tc.want)
			}
		})
	}
}

// TestTimeWindowNoUint64Wrap drives timestamps near math.MaxUint64. Adding the
// skew to now or to valid_until would wrap to a tiny number and produce a false
// NotYetValid/Expired; verification must use subtraction instead.
func TestTimeWindowNoUint64Wrap(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	base := goldenDescriptor()
	base.NotBefore = goldenNotBefore
	base.ValidUntil = math.MaxUint64
	frame := signForTest(t, privateKey, base)

	expect := goldenExpect()
	expect.NowMS = math.MaxUint64 - 10
	expect.MaxSkewMS = 100
	if _, result := Verify(frame, expect, publicKey); result != OK {
		t.Fatalf("Verify near MaxUint64 = %v, want OK (no wrap)", result)
	}

	// Expiry at exactly valid_until with zero skew is still Expired.
	base.NotBefore = math.MaxUint64 - 1
	base.ValidUntil = math.MaxUint64
	frame = signForTest(t, privateKey, base)
	expect.NowMS = math.MaxUint64
	expect.MaxSkewMS = 0
	if _, result := Verify(frame, expect, publicKey); result != Expired {
		t.Fatalf("Verify at MaxUint64 without skew = %v, want Expired", result)
	}
}

func TestRollbackFloor(t *testing.T) {
	cases := []struct {
		name     string
		minValid uint64
		want     Result
	}{
		{"below_floor", goldenValidUntil + 1, Rollback},
		{"at_floor", goldenValidUntil, OK},
		{"no_floor", 0, OK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expect := goldenExpect()
			expect.MinValidUntil = tc.minValid
			if _, result := Verify(goldenFrame(t), expect, goldenPublicKey(t)); result != tc.want {
				t.Fatalf("Verify(min_valid_until=%d) = %v, want %v", tc.minValid, result, tc.want)
			}
		})
	}
}

func TestStructuralFailuresAreFormat(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, frame []byte) []byte
	}{
		{"nil", func(t *testing.T, _ []byte) []byte { return nil }},
		{"empty", func(t *testing.T, _ []byte) []byte { return []byte{} }},
		{"truncated", func(t *testing.T, frame []byte) []byte { return frame[:frameBytes-1] }},
		{"trailing_byte", func(t *testing.T, frame []byte) []byte { return append(frame, 0) }},
		{"bad_magic", func(t *testing.T, frame []byte) []byte { frame[0] = 'X'; return frame }},
		{"schema_major", func(t *testing.T, frame []byte) []byte {
			binary.BigEndian.PutUint16(frame[offSchemaMajor:], schemaMajor+1)
			return frame
		}},
		{"schema_minor", func(t *testing.T, frame []byte) []byte {
			binary.BigEndian.PutUint16(frame[offSchemaMinor:], schemaMinor+1)
			return frame
		}},
		{"total_length", func(t *testing.T, frame []byte) []byte {
			binary.BigEndian.PutUint32(frame[offTotalLength:], frameBytes-1)
			return frame
		}},
		{"reserved_low", func(t *testing.T, frame []byte) []byte { frame[offReserved] = 1; return frame }},
		{"reserved_high", func(t *testing.T, frame []byte) []byte { frame[offReserved+1] = 1; return frame }},
		{"port_zero", func(t *testing.T, frame []byte) []byte {
			binary.BigEndian.PutUint16(frame[offPort:], 0)
			return frame
		}},
		{"text_bad_character", func(t *testing.T, frame []byte) []byte { frame[offIssuerID] = '/'; return frame }},
		{"text_empty", func(t *testing.T, frame []byte) []byte {
			for i := 0; i < textBytes; i++ {
				frame[offCoreID+i] = 0
			}
			return frame
		}},
		{"text_unterminated", func(t *testing.T, frame []byte) []byte {
			for i := 0; i < textBytes; i++ {
				frame[offCoreID+i] = 'a'
			}
			return frame
		}},
		{"text_padding_not_zero", func(t *testing.T, frame []byte) []byte {
			frame[offKeyID+len("core-key-2026a")] = 1
			return frame
		}},
		{"time_equal", func(t *testing.T, frame []byte) []byte {
			copy(frame[offValidUntil:offValidUntil+8], frame[offNotBefore:offNotBefore+8])
			return frame
		}},
		{"time_inverted", func(t *testing.T, frame []byte) []byte {
			binary.BigEndian.PutUint64(frame[offNotBefore:], goldenValidUntil+1)
			return frame
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := tc.mutate(t, goldenFrame(t))
			if _, result := Decode(frame); result != Format {
				t.Fatalf("Decode result = %v, want Format", result)
			}
			if _, result := Verify(frame, goldenExpect(), goldenPublicKey(t)); result != Format {
				t.Fatalf("Verify result = %v, want Format", result)
			}
		})
	}
}

func TestInvalidExpectIsFormat(t *testing.T) {
	zeroPin := goldenExpect()
	zeroPin.Pin = [pinBytes]byte{}
	tooMuchSkew := goldenExpect()
	tooMuchSkew.MaxSkewMS = maxSkewMS + 1

	cases := []struct {
		name   string
		expect Expect
	}{
		{"zero_pin", zeroPin},
		{"skew_above_limit", tooMuchSkew},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, result := Verify(goldenFrame(t), tc.expect, goldenPublicKey(t)); result != Format {
				t.Fatalf("Verify result = %v, want Format", result)
			}
		})
	}

	// Expect is validated before the frame and before the key, so a zero pin
	// never reaches a TOFU-style comparison.
	if _, result := Verify(nil, zeroPin, nil); result != Format {
		t.Fatalf("Verify(nil frame, zero pin) = %v, want Format", result)
	}

	// The boundary itself (60000) is accepted.
	boundary := goldenExpect()
	boundary.MaxSkewMS = maxSkewMS
	if _, result := Verify(goldenFrame(t), boundary, goldenPublicKey(t)); result != OK {
		t.Fatalf("Verify with skew = %d = %v, want OK", maxSkewMS, result)
	}
}

// TestNoTrustOnFirstUse proves that a bad signature is a hard failure on the
// very first verification and that no unsigned or unverified state is cached.
func TestNoTrustOnFirstUse(t *testing.T) {
	forged := goldenFrame(t)
	for i := offSignature; i < offSignature+signatureBytes; i++ {
		forged[i] = 0
	}
	if _, result := Verify(forged, goldenExpect(), goldenPublicKey(t)); result != Signature {
		t.Fatalf("first Verify of unsigned frame = %v, want Signature", result)
	}
	if _, result := Verify(goldenFrame(t), goldenExpect(), goldenPublicKey(t)); result != OK {
		t.Fatalf("Verify after refusal = %v, want OK", result)
	}

	// A frame signed by an untrusted key is rejected even if every field and
	// pin matches the expectation.
	_, untrustedPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	frame := signForTest(t, untrustedPrivate, goldenDescriptor())
	if _, result := Verify(frame, goldenExpect(), goldenPublicKey(t)); result != Signature {
		t.Fatalf("Verify untrusted signature = %v, want Signature", result)
	}
}

func signForTest(t *testing.T, privateKey ed25519.PrivateKey, descriptor Descriptor) []byte {
	t.Helper()
	frame := encodeForTest(t, descriptor)
	message := make([]byte, 0, len(domain)+unsignedBytes)
	message = append(message, domain...)
	message = append(message, frame[:unsignedBytes]...)
	copy(frame[offSignature:], ed25519.Sign(privateKey, message))
	return frame
}

func encodeForTest(t *testing.T, descriptor Descriptor) []byte {
	t.Helper()
	frame := make([]byte, frameBytes)
	copy(frame[offMagic:], magic[:])
	binary.BigEndian.PutUint16(frame[offSchemaMajor:], schemaMajor)
	binary.BigEndian.PutUint16(frame[offSchemaMinor:], schemaMinor)
	binary.BigEndian.PutUint32(frame[offTotalLength:], frameBytes)
	binary.BigEndian.PutUint16(frame[offPort:], descriptor.Port)
	writeTestText(t, frame[offCoreID:], descriptor.CoreID)
	writeTestText(t, frame[offIssuerID:], descriptor.IssuerID)
	writeTestText(t, frame[offKeyID:], descriptor.KeyID)
	writeTestText(t, frame[offScope:], descriptor.Scope)
	writeTestText(t, frame[offEndpoint:], descriptor.EndpointHost)
	binary.BigEndian.PutUint64(frame[offNotBefore:], descriptor.NotBefore)
	binary.BigEndian.PutUint64(frame[offValidUntil:], descriptor.ValidUntil)
	copy(frame[offPin:], descriptor.SPKIPin[:])
	return frame
}

func writeTestText(t *testing.T, slot []byte, value string) {
	t.Helper()
	if len(value) == 0 || len(value) > textBytes {
		t.Fatalf("test text %q has length %d, want 1..%d", value, len(value), textBytes)
	}
	if bytes.IndexByte([]byte(value), 0) >= 0 {
		t.Fatalf("test text %q contains NUL", value)
	}
	for _, b := range []byte(value) {
		if !tokenByte(b) {
			t.Fatalf("test text %q contains non-token byte %q", value, b)
		}
	}
	copy(slot, value)
}
