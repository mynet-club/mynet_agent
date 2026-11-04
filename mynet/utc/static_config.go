package utc

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
)

// StaticTrustFile is installed by the trusted operator/control plane, never by
// a lease sender. It contains public keys and exact local binding expectations.
type StaticTrustFile struct {
	Version             uint32                       `json:"version"`
	LeaseDirectory      string                       `json:"lease_directory"`
	RevocationDirectory string                       `json:"revocation_directory"`
	StateDirectory      string                       `json:"state_directory"`
	Issuers             map[string]StaticTrustIssuer `json:"issuers"`
	Pairs               []StaticPairRule             `json:"pairs"`
}
type StaticTrustIssuer struct {
	IssuerID         string   `json:"issuer_id"`
	PublicKey        string   `json:"public_key"` // 32-byte lowercase hex; no private key
	Scopes           []string `json:"scopes"`
	MinimumRevision  uint64   `json:"minimum_revision"`
	MaxLeaseTTL      uint64   `json:"max_lease_ttl"`
	MaxRevocationTTL uint64   `json:"max_revocation_ttl"`
}

func LoadStaticAuthority(path string) (*StaticAuthority, error) {
	var file StaticTrustFile
	if err := readCanonicalPrivate(path, &file, 1048576, true); err != nil {
		return nil, err
	}
	if file.Version != 1 {
		return nil, errors.New("unsupported UTC static trust schema")
	}
	config := StaticAuthorityConfig{LeaseDirectory: file.LeaseDirectory, RevocationDirectory: file.RevocationDirectory, StateDirectory: file.StateDirectory, Pairs: file.Pairs, Issuers: map[string]TrustedStaticIssuer{}}
	for id, issuer := range file.Issuers {
		if !canonicalHex(issuer.PublicKey, ed25519.PublicKeySize) {
			return nil, errors.New("invalid UTC issuer public key")
		}
		public, _ := hex.DecodeString(issuer.PublicKey)
		config.Issuers[id] = TrustedStaticIssuer{IssuerID: issuer.IssuerID, PublicKey: ed25519.PublicKey(public), Scopes: issuer.Scopes, MinimumRevision: issuer.MinimumRevision, MaxLeaseTTL: issuer.MaxLeaseTTL, MaxRevocationTTL: issuer.MaxRevocationTTL}
	}
	return NewStaticAuthority(config)
}
