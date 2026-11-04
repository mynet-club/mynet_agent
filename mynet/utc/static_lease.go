package utc

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// StaticLease is the signed static authorization consumed by A2. The data
// service must independently enforce the same lease, identity and byte budget.
// No VPN identity/key is used to authenticate UTC transport identities.
type StaticLease struct {
	Version           uint32 `json:"version"`
	IssuerID          string `json:"issuer_id"`
	KeyID             string `json:"key_id"`
	LeaseID           string `json:"lease_id"`
	Scope             string `json:"scope"`
	SubjectID         string `json:"subject_id"`
	SourceTransportID string `json:"source_transport_id"`
	ProviderID        string `json:"provider_id"`
	TargetPeerID      uint64 `json:"target_peer_id"`
	PairID            uint64 `json:"pair_id"`
	PairGen           uint64 `json:"pair_gen"`
	Manifest          string `json:"manifest"`
	BindingID         string `json:"binding_id"`
	Direction         string `json:"direction"`
	EndpointScope     string `json:"endpoint_scope"` // SHA-256 of the trusted fixed binding descriptor
	IssuedAt          uint64 `json:"issued_at"`
	NotBefore         uint64 `json:"not_before"`
	ExpiresAt         uint64 `json:"expires_at"`
	ByteLimit         uint64 `json:"byte_limit"`
	QuotaAccountID    string `json:"quota_account_id"`
	QuotaOwner        string `json:"quota_owner"`
	RevocationEpoch   uint64 `json:"revocation_epoch"`
	Nonce             string `json:"nonce"` // 16-byte canonical hex, not a one-shot handshake token
	Signature         string `json:"signature,omitempty"`
}

type StaticRevocations struct {
	Version         uint32   `json:"version"`
	IssuerID        string   `json:"issuer_id"`
	KeyID           string   `json:"key_id"`
	Scope           string   `json:"scope"`
	Revision        uint64   `json:"revision"`
	IssuedAt        uint64   `json:"issued_at"`
	ExpiresAt       uint64   `json:"expires_at"`
	RevokedLeaseIDs []string `json:"revoked_lease_ids"` // strictly sorted, duplicate-free
	Signature       string   `json:"signature,omitempty"`
}

func canonicalHex(s string, size int) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == size && hex.EncodeToString(raw) == s
}
func validDirection(direction string) bool { return direction == "a-to-b" || direction == "b-to-a" }

// Signing bytes are domain-separated canonical JSON of the versioned typed
// payload, excluding signature. Every string is bounded ASCII or canonical hex;
// integers are uint64 decimal, never a native struct/padding or floating point.
func StaticLeaseSigningBytes(lease StaticLease) ([]byte, error) {
	if lease.Version != 1 || lease.TargetPeerID == 0 || lease.PairID == 0 || lease.PairGen == 0 || lease.ByteLimit == 0 || lease.RevocationEpoch == 0 || lease.IssuedAt == 0 || lease.NotBefore < lease.IssuedAt || lease.ExpiresAt <= lease.NotBefore || !validDirection(lease.Direction) || !canonicalHex(lease.Manifest, 32) || !canonicalHex(lease.EndpointScope, 32) || !canonicalHex(lease.Nonce, 16) {
		return nil, errors.New("invalid static UTC lease")
	}
	for _, value := range []string{lease.IssuerID, lease.KeyID, lease.LeaseID, lease.Scope, lease.SubjectID, lease.SourceTransportID, lease.ProviderID, lease.BindingID, lease.QuotaAccountID, lease.QuotaOwner} {
		if !token(value) {
			return nil, errors.New("invalid static UTC lease identity")
		}
	}
	lease.Signature = ""
	raw, err := json.Marshal(lease)
	if err != nil {
		return nil, err
	}
	return append([]byte("mynet.utc.static-lease.v1\x00"), raw...), nil
}
func StaticRevocationSigningBytes(snapshot StaticRevocations) ([]byte, error) {
	if snapshot.Version != 1 || !token(snapshot.IssuerID) || !token(snapshot.KeyID) || !token(snapshot.Scope) || snapshot.Revision == 0 || snapshot.IssuedAt == 0 || snapshot.ExpiresAt <= snapshot.IssuedAt || len(snapshot.RevokedLeaseIDs) > 1024 {
		return nil, errors.New("invalid static UTC revocation snapshot")
	}
	for i, id := range snapshot.RevokedLeaseIDs {
		if !token(id) || i > 0 && snapshot.RevokedLeaseIDs[i-1] >= id {
			return nil, errors.New("noncanonical revoked lease IDs")
		}
	}
	snapshot.Signature = ""
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return append([]byte("mynet.utc.static-revocations.v1\x00"), raw...), nil
}
func verifyStaticSignature(key ed25519.PublicKey, payload []byte, signature string) bool {
	if len(key) != ed25519.PublicKeySize || !canonicalHex(signature, ed25519.SignatureSize) {
		return false
	}
	raw, _ := hex.DecodeString(signature)
	return ed25519.Verify(key, payload, raw)
}

// Empty revocation lists have one wire representation: [], never null.
func (snapshot StaticRevocations) MarshalJSON() ([]byte, error) {
	type plain StaticRevocations
	if snapshot.RevokedLeaseIDs == nil {
		snapshot.RevokedLeaseIDs = []string{}
	}
	return json.Marshal(plain(snapshot))
}
