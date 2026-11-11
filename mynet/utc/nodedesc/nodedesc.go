// Package nodedesc decodes and verifies the frozen UTC NodeDescriptor frame
// issued by utc-core.
//
// The frame is exactly 452 bytes and is byte-compatible with the C contract in
// mynet_utc (mn_utc_node_descriptor_t). All integers are big-endian:
//
//	 0    magic "UTCNODS1"
//	 8    schema major u16 = 1
//	10    schema minor u16 = 0
//	12    total length u32 = 452
//	16    port u16 (non-zero)
//	18    reserved u16 = 0
//	20    core_id text[64]
//	84    issuer_id text[64]
//	148   key_id text[64]
//	212   scope text[64]
//	276   endpoint_host text[64]
//	340   not_before u64 (milliseconds since the Unix epoch)
//	348   valid_until u64 (milliseconds since the Unix epoch)
//	356   spki_pin[32]
//	388   signature[64]
//
// A text slot is a non-empty printable token [A-Za-z0-9_.-], NUL-terminated,
// with every byte after the NUL zero. The signature is Ed25519 over the domain
// string "mynet.utc.access.node-descriptor.v1" (without its NUL) followed by
// frame[0:388]; the signature slot itself is not covered and carries no length
// prefix.
//
// Decode only proves encoding and intrinsic field consistency. Verify adds
// signature, time window, issuer/key/scope binding, SPKI pin equality and a
// valid-until floor. There is deliberately no trust-on-first-use path and no
// unsigned fallback: a caller that lacks a pinned Core key and a matching
// pin/binding tuple must refuse the descriptor.
//
// Time windows take a caller-supplied clock skew grace of at most 60000 ms.
// A frame is NotYetValid when now+skew < not_before and Expired when
// now >= valid_until+skew; both comparisons use subtraction instead of
// addition, so timestamps near math.MaxUint64 neither wrap nor produce a false
// boundary result.
package nodedesc

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
)

// Frozen frame geometry and domain string. They mirror
// MN_UTC_NODE_DESCRIPTOR_*, MN_UTC_ACCESS_OFF_* and MN_UTC_ACCESS_DOMAIN_NODE in
// mynet_utc. Note that the C header encodes the schema as two u16 values and
// the total length as u32, which is what the golden frame carries.
const (
	frameBytes     = 452
	unsignedBytes  = 388
	textBytes      = 64
	pinBytes       = 32
	signatureBytes = 64

	schemaMajor = 1
	schemaMinor = 0

	domain    = "mynet.utc.access.node-descriptor.v1"
	maxSkewMS = 60000
)

// Frame offsets. They are constants rather than a struct decode so the wire
// layout is independent of Go memory layout.
const (
	offMagic       = 0
	offSchemaMajor = 8
	offSchemaMinor = 10
	offTotalLength = 12
	offPort        = 16
	offReserved    = 18
	offText        = 20
	offCoreID      = offText
	offIssuerID    = offCoreID + textBytes
	offKeyID       = offIssuerID + textBytes
	offScope       = offKeyID + textBytes
	offEndpoint    = offScope + textBytes
	offNotBefore   = offEndpoint + textBytes
	offValidUntil  = offNotBefore + 8
	offPin         = offValidUntil + 8
	offSignature   = offPin + pinBytes
)

var magic = [8]byte{'U', 'T', 'C', 'N', 'O', 'D', 'S', '1'}

// Descriptor is a decoded NodeDescriptor. Times are milliseconds since the Unix
// epoch, matching the wire fields without unit conversion.
type Descriptor struct {
	CoreID       string
	IssuerID     string
	KeyID        string
	Scope        string
	EndpointHost string
	Port         uint16
	NotBefore    uint64
	ValidUntil   uint64
	SPKIPin      [32]byte
}

// Expect is the local trust context applied by Verify. IssuerID, KeyID and
// Scope are compared exactly (case-sensitive); Pin is the expected Core SPKI
// pin; NowMS is the verifier clock in milliseconds; MaxSkewMS is the accepted
// clock skew (at most 60000); MinValidUntil is the rollback floor, compared
// against the frame's valid_until.
type Expect struct {
	IssuerID      string
	KeyID         string
	Scope         string
	Pin           [32]byte
	NowMS         uint64
	MaxSkewMS     uint64
	MinValidUntil uint64
}

// Result classifies the outcome of Decode and Verify. Only OK accepts the
// descriptor; every other value must be treated as a refusal.
type Result int

const (
	// OK means the frame is structurally valid and every Verify check passed.
	OK Result = iota
	// Format means the frame or the Expect context is malformed: wrong length,
	// magic, schema version, total length, reserved bytes, text slot, port,
	// time ordering, an all-zero expected pin, or MaxSkewMS > 60000.
	Format
	// Signature means the Ed25519 signature did not verify under the supplied
	// public key, including a key of the wrong size.
	Signature
	// NotYetValid means now+skew < not_before.
	NotYetValid
	// Expired means now >= valid_until+skew.
	Expired
	// Binding means issuer_id, key_id or scope differs from Expect.
	Binding
	// Pin means the frame's spki_pin differs from Expect.Pin.
	Pin
	// Rollback means the frame's valid_until is below Expect.MinValidUntil.
	Rollback
)

// ResultName returns a stable diagnostic name for a Result.
func ResultName(result Result) string {
	switch result {
	case OK:
		return "ok"
	case Format:
		return "format"
	case Signature:
		return "signature"
	case NotYetValid:
		return "not_yet_valid"
	case Expired:
		return "expired"
	case Binding:
		return "binding"
	case Pin:
		return "pin"
	case Rollback:
		return "rollback"
	default:
		return "unknown"
	}
}

// Decode validates the frame's structure and intrinsic fields and returns the
// decoded descriptor. It performs no cryptography and no time, binding or pin
// checks. Unknown schema versions are Format because this contract has no
// forward-compatible parse path. On any failure the returned Descriptor is the
// zero value, so callers never act on partially decoded data.
func Decode(frame []byte) (Descriptor, Result) {
	if !headerValid(frame) {
		return Descriptor{}, Format
	}
	if !zeroBytes(frame[offReserved : offReserved+2]) {
		return Descriptor{}, Format
	}

	var descriptor Descriptor
	var ok bool
	if descriptor.CoreID, ok = readText(frame[offCoreID : offCoreID+textBytes]); !ok {
		return Descriptor{}, Format
	}
	if descriptor.IssuerID, ok = readText(frame[offIssuerID : offIssuerID+textBytes]); !ok {
		return Descriptor{}, Format
	}
	if descriptor.KeyID, ok = readText(frame[offKeyID : offKeyID+textBytes]); !ok {
		return Descriptor{}, Format
	}
	if descriptor.Scope, ok = readText(frame[offScope : offScope+textBytes]); !ok {
		return Descriptor{}, Format
	}
	if descriptor.EndpointHost, ok = readText(frame[offEndpoint : offEndpoint+textBytes]); !ok {
		return Descriptor{}, Format
	}

	descriptor.Port = binary.BigEndian.Uint16(frame[offPort : offPort+2])
	if descriptor.Port == 0 {
		return Descriptor{}, Format
	}
	descriptor.NotBefore = binary.BigEndian.Uint64(frame[offNotBefore : offNotBefore+8])
	descriptor.ValidUntil = binary.BigEndian.Uint64(frame[offValidUntil : offValidUntil+8])
	if descriptor.ValidUntil <= descriptor.NotBefore {
		return Descriptor{}, Format
	}
	copy(descriptor.SPKIPin[:], frame[offPin:offPin+pinBytes])
	return descriptor, OK
}

// Verify decodes frame and applies the full node-side trust contract in this
// order: structure (Format), Ed25519 signature (Signature), time window
// (NotYetValid/Expired), issuer/key/scope binding (Binding), SPKI pin equality
// (Pin), valid_until floor (Rollback), then OK. Expect is validated first:
// an all-zero Pin or MaxSkewMS > 60000 is Format. A wrong-sized or nil public
// key is Signature, never a panic and never a soft failure. On any non-OK
// result the returned Descriptor is the zero value.
func Verify(frame []byte, expect Expect, publicKey ed25519.PublicKey) (Descriptor, Result) {
	if expect.MaxSkewMS > maxSkewMS || expect.Pin == ([pinBytes]byte{}) {
		return Descriptor{}, Format
	}
	descriptor, result := Decode(frame)
	if result != OK {
		return Descriptor{}, result
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return Descriptor{}, Signature
	}

	message := make([]byte, 0, len(domain)+unsignedBytes)
	message = append(message, domain...)
	message = append(message, frame[:unsignedBytes]...)
	if !ed25519.Verify(publicKey, message, frame[unsignedBytes:]) {
		return Descriptor{}, Signature
	}

	// not_before > now && not_before-now > skew  <=>  now+skew < not_before,
	// without computing now+skew (which could wrap).
	if descriptor.NotBefore > expect.NowMS &&
		descriptor.NotBefore-expect.NowMS > expect.MaxSkewMS {
		return Descriptor{}, NotYetValid
	}
	// now >= valid_until && now-valid_until >= skew  <=>  now >= valid_until+skew,
	// without computing valid_until+skew (which could wrap).
	if expect.NowMS >= descriptor.ValidUntil &&
		expect.NowMS-descriptor.ValidUntil >= expect.MaxSkewMS {
		return Descriptor{}, Expired
	}

	if descriptor.IssuerID != expect.IssuerID ||
		descriptor.KeyID != expect.KeyID ||
		descriptor.Scope != expect.Scope {
		return Descriptor{}, Binding
	}
	if descriptor.SPKIPin != expect.Pin {
		return Descriptor{}, Pin
	}
	if descriptor.ValidUntil < expect.MinValidUntil {
		return Descriptor{}, Rollback
	}
	return descriptor, OK
}

func headerValid(frame []byte) bool {
	if len(frame) != frameBytes {
		return false
	}
	if !bytes.Equal(frame[offMagic:offMagic+len(magic)], magic[:]) {
		return false
	}
	if binary.BigEndian.Uint16(frame[offSchemaMajor:]) != schemaMajor {
		return false
	}
	if binary.BigEndian.Uint16(frame[offSchemaMinor:]) != schemaMinor {
		return false
	}
	return binary.BigEndian.Uint32(frame[offTotalLength:]) == frameBytes
}

// readText accepts exactly one non-empty token followed by zero padding.
func readText(slot []byte) (string, bool) {
	length := 0
	for length < textBytes && slot[length] != 0 {
		if !tokenByte(slot[length]) {
			return "", false
		}
		length++
	}
	if length == 0 || length == textBytes {
		return "", false
	}
	if !zeroBytes(slot[length:]) {
		return "", false
	}
	return string(slot[:length]), true
}

func tokenByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '_' || b == '-' || b == '.'
}

func zeroBytes(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
