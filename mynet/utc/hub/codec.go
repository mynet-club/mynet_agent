package hub

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"net/netip"
	"strconv"
)

// Canonical binary layouts frozen by mynet_utc docs/M4_00_HUB_DAEMON_DESIGN.md
// and src/hub/grant.c / src/hub/target.c. Offsets are byte positions in the
// fixed-width network-byte-order records.
const (
	grantBytes        = 826
	grantUnsigned     = 762
	grantText         = 64
	grantSideOffset   = 12
	grantTotalOffset  = 14
	grantTextOffset   = 18
	grantPairIDOff    = 658
	grantPairGenOff   = 666
	grantLeaseOff     = 674
	grantPlacementOff = 682
	grantQuotaEpoch   = 690
	grantByteLimitOff = 698
	grantIssuedAtOff  = 706
	grantNotBeforeOff = 714
	grantValidOff     = 722
	grantDigestOff    = 730
	grantSignatureOff = 762

	bindingBytes        = 500
	bindingUnsigned     = 436
	bindingKindOffset   = 12
	bindingFamilyOffset = 13
	bindingSourceOffset = 14
	bindingPeerOffset   = 15
	bindingTotalOffset  = 16
	bindingPortOffset   = 20
	bindingScopeOff     = 22
	bindingReservedOff  = 26
	bindingTextOffset   = 28
	bindingPairIDOff    = 412
	bindingPairGenOff   = 420
	bindingPlacementOff = 428
	bindingSignatureOff = 436

	bindingKindAS = 1
	bindingKindAD = 2
)

var (
	grantMagic   = [8]byte{'U', 'T', 'C', 'H', 'G', 'R', 'T', '2'}
	bindingMagic = [8]byte{'U', 'T', 'C', 'H', 'B', 'N', 'D', '2'}

	grantDomain   = []byte("mynet.utc.hub.data-grant.v2")
	bindingDomain = []byte("mynet.utc.hub.target-binding.v2")
)

// Grant is the decoded DataGrant v2. Signature is verified only when the
// client has an issuer public key configured.
type Grant struct {
	GrantID           string
	IssuerID          string
	KeyID             string
	Scope             string
	ClientTransportID string
	EdgeID            string
	ShardID           string
	ReactorID         string
	QuotaAccountID    string
	QuotaSliceID      string

	PairID         uint64
	PairGen        uint64
	LeaseEpoch     uint64
	PlacementEpoch uint64
	QuotaEpoch     uint64
	ByteLimit      uint64
	IssuedAt       uint64
	NotBefore      uint64
	ValidUntil     uint64

	Side                uint8
	TargetBindingDigest [32]byte
	Signature           [64]byte

	raw []byte
}

// Binding is the decoded 500-byte TargetBinding v2.
type Binding struct {
	BindingID       string
	IssuerID        string
	KeyID           string
	Scope           string
	PeerTransportID string
	Address         string

	PairID         uint64
	PairGen        uint64
	PlacementEpoch uint64
	Port           uint16
	AddressScopeID uint32
	Kind           uint8
	Family         uint8
	SourceSide     uint8
	PeerSide       uint8
	Signature      [64]byte

	raw []byte
}

func readSlot(raw []byte, offset int) (string, error) {
	slot := raw[offset : offset+grantText]
	end := bytes.IndexByte(slot, 0)
	if end <= 0 {
		return "", fmt.Errorf("canonical text slot is empty or unterminated")
	}
	for _, b := range slot[end:] {
		if b != 0 {
			return "", fmt.Errorf("canonical text slot has non-zero padding")
		}
	}
	if !tokenBytes(slot[:end]) {
		return "", fmt.Errorf("canonical text slot has invalid characters")
	}
	return string(slot[:end]), nil
}

func tokenBytes(value []byte) bool {
	if len(value) == 0 || len(value) >= grantText {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

func addressBytes(value []byte) bool {
	if len(value) >= grantText {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' ||
			c == ':' || c == '.') {
			return false
		}
	}
	return true
}

func readAddressSlot(raw []byte, offset int) (string, error) {
	slot := raw[offset : offset+grantText]
	end := bytes.IndexByte(slot, 0)
	if end < 0 {
		return "", fmt.Errorf("address slot is unterminated")
	}
	for _, b := range slot[end:] {
		if b != 0 {
			return "", fmt.Errorf("address slot has non-zero padding")
		}
	}
	if end == 0 {
		return "", nil
	}
	if !addressBytes(slot[:end]) {
		return "", fmt.Errorf("address slot has invalid characters")
	}
	return string(slot[:end]), nil
}

func u16(raw []byte, offset int) uint16 { return uint16(raw[offset])<<8 | uint16(raw[offset+1]) }
func u32(raw []byte, offset int) uint32 {
	return uint32(raw[offset])<<24 | uint32(raw[offset+1])<<16 |
		uint32(raw[offset+2])<<8 | uint32(raw[offset+3])
}
func u64(raw []byte, offset int) uint64 {
	var value uint64
	for _, b := range raw[offset : offset+8] {
		value = value<<8 | uint64(b)
	}
	return value
}

func digestNonZero(digest []byte) bool {
	var combined byte
	for _, b := range digest {
		combined |= b
	}
	return combined != 0
}

func decodeGrant(raw []byte) (Grant, error) {
	var grant Grant
	if len(raw) != grantBytes {
		return grant, fmt.Errorf("DataGrant must be %d bytes, got %d", grantBytes, len(raw))
	}
	if !bytes.Equal(raw[:8], grantMagic[:]) || u32(raw, grantTotalOffset) != grantBytes ||
		raw[13] != 0 {
		return grant, fmt.Errorf("DataGrant header is invalid")
	}
	if u16(raw, 8) != 2 || u16(raw, 10) != 0 {
		return grant, fmt.Errorf("DataGrant schema is unsupported")
	}
	tokens := []*string{
		&grant.GrantID, &grant.IssuerID, &grant.KeyID, &grant.Scope,
		&grant.ClientTransportID, &grant.EdgeID, &grant.ShardID,
		&grant.ReactorID, &grant.QuotaAccountID, &grant.QuotaSliceID,
	}
	for i, target := range tokens {
		value, err := readSlot(raw, grantTextOffset+i*grantText)
		if err != nil {
			return Grant{}, fmt.Errorf("DataGrant token %d: %w", i, err)
		}
		*target = value
	}
	grant.Side = raw[grantSideOffset]
	grant.PairID = u64(raw, grantPairIDOff)
	grant.PairGen = u64(raw, grantPairGenOff)
	grant.LeaseEpoch = u64(raw, grantLeaseOff)
	grant.PlacementEpoch = u64(raw, grantPlacementOff)
	grant.QuotaEpoch = u64(raw, grantQuotaEpoch)
	grant.ByteLimit = u64(raw, grantByteLimitOff)
	grant.IssuedAt = u64(raw, grantIssuedAtOff)
	grant.NotBefore = u64(raw, grantNotBeforeOff)
	grant.ValidUntil = u64(raw, grantValidOff)
	copy(grant.TargetBindingDigest[:], raw[grantDigestOff:grantSignatureOff])
	copy(grant.Signature[:], raw[grantSignatureOff:])
	grant.raw = raw
	if err := grant.validate(); err != nil {
		return Grant{}, err
	}
	return grant, nil
}

func (grant Grant) validate() error {
	if grant.PairID == 0 || grant.PairGen == 0 || grant.LeaseEpoch == 0 ||
		grant.PlacementEpoch == 0 || grant.QuotaEpoch == 0 || grant.ByteLimit == 0 ||
		grant.IssuedAt == 0 || grant.NotBefore < grant.IssuedAt ||
		grant.ValidUntil <= grant.NotBefore || grant.Side > 1 ||
		!digestNonZero(grant.TargetBindingDigest[:]) {
		return fmt.Errorf("DataGrant fields are invalid")
	}
	return nil
}

func decodeBinding(raw []byte) (Binding, error) {
	var binding Binding
	if len(raw) != bindingBytes {
		return binding, fmt.Errorf("TargetBinding must be %d bytes, got %d", bindingBytes, len(raw))
	}
	if !bytes.Equal(raw[:8], bindingMagic[:]) || u32(raw, bindingTotalOffset) != bindingBytes ||
		raw[bindingReservedOff] != 0 || raw[bindingReservedOff+1] != 0 {
		return binding, fmt.Errorf("TargetBinding header is invalid")
	}
	if u16(raw, 8) != 2 || u16(raw, 10) != 0 {
		return binding, fmt.Errorf("TargetBinding schema is unsupported")
	}
	binding.Kind = raw[bindingKindOffset]
	binding.Family = raw[bindingFamilyOffset]
	binding.SourceSide = raw[bindingSourceOffset]
	binding.PeerSide = raw[bindingPeerOffset]
	binding.Port = u16(raw, bindingPortOffset)
	binding.AddressScopeID = u32(raw, bindingScopeOff)
	binding.PairID = u64(raw, bindingPairIDOff)
	binding.PairGen = u64(raw, bindingPairGenOff)
	binding.PlacementEpoch = u64(raw, bindingPlacementOff)
	tokens := []*string{
		&binding.BindingID, &binding.IssuerID, &binding.KeyID,
		&binding.Scope, &binding.PeerTransportID,
	}
	for i, target := range tokens {
		value, err := readSlot(raw, bindingTextOffset+i*grantText)
		if err != nil {
			return Binding{}, fmt.Errorf("TargetBinding token %d: %w", i, err)
		}
		*target = value
	}
	address, err := readAddressSlot(raw, bindingTextOffset+5*grantText)
	if err != nil {
		return Binding{}, err
	}
	binding.Address = address
	copy(binding.Signature[:], raw[bindingSignatureOff:])
	binding.raw = raw
	if err := binding.validate(); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

func (binding Binding) validate() error {
	if binding.PairID == 0 || binding.PairGen == 0 || binding.PlacementEpoch == 0 {
		return fmt.Errorf("TargetBinding identity is invalid")
	}
	if binding.SourceSide > 1 || binding.PeerSide > 1 || binding.SourceSide == binding.PeerSide {
		return fmt.Errorf("TargetBinding sides are invalid")
	}
	switch binding.Kind {
	case bindingKindAS:
		if binding.Family != 4 && binding.Family != 6 {
			return fmt.Errorf("A-S binding family is invalid")
		}
		if binding.Port == 0 || binding.Address == "" {
			return fmt.Errorf("A-S binding endpoint is missing")
		}
		address, err := netip.ParseAddr(binding.Address)
		if err != nil || address.Zone() != "" || address.Is4In6() ||
			address.IsUnspecified() || address.IsMulticast() ||
			address.Is4() && binding.Family != 4 || address.Is6() && binding.Family != 6 {
			return fmt.Errorf("A-S binding address is not a unicast endpoint")
		}
		if binding.Family == 4 && binding.AddressScopeID != 0 {
			return fmt.Errorf("IPv4 binding has an IPv6 scope")
		}
	case bindingKindAD:
		if binding.Family != 0 || binding.Address != "" || binding.Port != 0 ||
			binding.AddressScopeID != 0 {
			return fmt.Errorf("A-D binding has endpoint fields")
		}
	default:
		return fmt.Errorf("TargetBinding kind is unsupported")
	}
	return nil
}

func verifyGrantSignature(grant Grant, publicKey ed25519.PublicKey) bool {
	message := make([]byte, 0, len(grantDomain)+grantUnsigned)
	message = append(message, grantDomain...)
	message = append(message, grant.raw[:grantUnsigned]...)
	return ed25519.Verify(publicKey, message, grant.Signature[:])
}

func verifyBindingSignature(binding Binding, publicKey ed25519.PublicKey) bool {
	message := make([]byte, 0, len(bindingDomain)+bindingUnsigned)
	message = append(message, bindingDomain...)
	message = append(message, binding.raw[:bindingUnsigned]...)
	return ed25519.Verify(publicKey, message, binding.Signature[:])
}

// checkBindingAgainstGrant enforces the frozen binding relation: the grant's
// target digest is SHA-256 over the complete 500-byte binding, and the binding
// identity equals the granted pair/side/placement.
func checkBindingAgainstGrant(grant Grant, binding Binding) error {
	digest := sha256.Sum256(binding.raw)
	if !bytes.Equal(digest[:], grant.TargetBindingDigest[:]) {
		return fmt.Errorf("TargetBinding digest does not match the DataGrant")
	}
	if binding.Scope != grant.Scope || binding.PairID != grant.PairID ||
		binding.PairGen != grant.PairGen || binding.PlacementEpoch != grant.PlacementEpoch ||
		binding.SourceSide != grant.Side {
		return fmt.Errorf("TargetBinding identity does not match the DataGrant")
	}
	return nil
}

func parseGrantID(value string) (uint32, bool) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	return uint32(parsed), err == nil
}
