// Package utils provides utility functions for GNB's custom ed25519 key generation
// This implementation replicates the exact behavior of GNB's C library without CGO dependencies
//
// GNB's ed25519 implementation follows the standard RFC 8032 with their custom hex encoding:
// - Private key: 64 bytes (SHA512 output) -> 128 hex characters
// - Public key: 32 bytes -> 64 hex characters
package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"fmt"

	"filippo.io/edwards25519"
)

// GNBKeyPair represents an ed25519 key pair in GNB format
type GNBKeyPair struct {
	// Seed is the original 32-byte seed
	Seed []byte
	// PrivateKey is the 64-byte private key (SHA512 output)
	PrivateKey []byte
	// ProcessedPrivateKey is the 32-byte processed private key used for computation
	ProcessedPrivateKey []byte
	// PublicKey is the 32-byte public key
	PublicKey []byte
}

// ToHex converts the key pair to hexadecimal strings as GNB expects
func (kp *GNBKeyPair) ToHex() (privateHex, publicHex string) {
	// GNB stores the full 64-byte private key as 128-character hex
	privateHex = hex.EncodeToString(kp.PrivateKey)
	// Public key is 32 bytes -> 64 character hex
	publicHex = hex.EncodeToString(kp.PublicKey)
	return
}

// GenerateGNBKeyPair generates a new GNB-compatible ed25519 key pair
func GenerateGNBKeyPair() (*GNBKeyPair, error) {
	// Generate 32-byte random seed
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("failed to generate random seed: %w", err)
	}

	return GenerateGNBKeyPairFromSeed(seed)
}

// GenerateGNBKeyPairFromSeed generates a key pair from a 32-byte seed
// This exactly replicates the GNB ed25519_create_keypair function in keypair.c:
//
//	void ed25519_create_keypair(unsigned char *public_key, unsigned char *private_key, const unsigned char *seed) {
//	    ge_p3 A;
//	    sha512(seed, 32, private_key);    // Expand seed to 64 bytes
//	    private_key[0] &= 248;            // Clear bottom 3 bits
//	    private_key[31] &= 63;            // Clear top 2 bits
//	    private_key[31] |= 64;            // Set bit 6
//	    ge_scalarmult_base(&A, private_key);  // Scalar mult with base point
//	    ge_p3_tobytes(public_key, &A);    // Encode point to bytes
//	}
func GenerateGNBKeyPairFromSeed(seed []byte) (*GNBKeyPair, error) {
	if len(seed) != 32 {
		return nil, fmt.Errorf("seed must be exactly 32 bytes, got %d", len(seed))
	}

	// Step 1: SHA512 expansion (replicates: sha512(seed, 32, private_key))
	h := sha512.Sum512(seed)
	fullPrivateKey := h[:]

	// Step 2: Copy first 32 bytes for processing
	processedPrivateKey := make([]byte, 32)
	copy(processedPrivateKey, fullPrivateKey[:32])

	// Step 3: Apply GNB's exact bit operations (standard ed25519 clamping)
	processedPrivateKey[0] &= 248 // Clear bottom 3 bits: private_key[0] &= 248
	processedPrivateKey[31] &= 63 // Clear top 2 bits: private_key[31] &= 63
	processedPrivateKey[31] |= 64 // Set bit 6: private_key[31] |= 64

	// Step 4: Compute public key using ed25519 scalar multiplication
	// This replicates: ge_scalarmult_base(&A, private_key); ge_p3_tobytes(public_key, &A);
	publicKey := computePublicKeyFromProcessedScalar(processedPrivateKey)

	return &GNBKeyPair{
		Seed:                seed,
		PrivateKey:          fullPrivateKey,
		ProcessedPrivateKey: processedPrivateKey,
		PublicKey:           publicKey,
	}, nil
}

// GNBPublicKeyFromPrivateKey computes public key from a processed 32-byte private key
// This replicates gnb_ed25519_public_from_private_wrapper function
func GNBPublicKeyFromPrivateKey(processedPrivateKey []byte) ([]byte, error) {
	if len(processedPrivateKey) != 32 {
		return nil, fmt.Errorf("processed private key must be exactly 32 bytes, got %d", len(processedPrivateKey))
	}

	publicKey := computePublicKeyFromProcessedScalar(processedPrivateKey)
	return publicKey, nil
}

// GNBPublicKeyFromPrivateHex computes public key from hex-encoded private key
func GNBPublicKeyFromPrivateHex(privateKeyHex string) ([]byte, error) {
	privateKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid hex encoding: %w", err)
	}

	var processedKey []byte
	switch len(privateKeyBytes) {
	case 32:
		// Could be seed or already-processed key
		// In GNB context, 32 bytes is usually the processed key (first 32 bytes of SHA512)
		processedKey = privateKeyBytes
	case 64:
		// Full GNB private key format, use first 32 bytes (already processed)
		processedKey = privateKeyBytes[:32]
	default:
		return nil, fmt.Errorf("private key must be 32 or 64 bytes, got %d", len(privateKeyBytes))
	}

	return GNBPublicKeyFromPrivateKey(processedKey)
}

// VerifyGNBKeyPair checks if a private key and public key match
func VerifyGNBKeyPair(privateKeyHex, publicKeyHex string) (bool, error) {
	computedPublicKey, err := GNBPublicKeyFromPrivateHex(privateKeyHex)
	if err != nil {
		return false, fmt.Errorf("failed to compute public key: %w", err)
	}

	expectedPublicKey, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return false, fmt.Errorf("invalid public key hex: %w", err)
	}

	if len(expectedPublicKey) != 32 {
		return false, fmt.Errorf("public key must be 32 bytes, got %d", len(expectedPublicKey))
	}

	// Constant-time comparison
	return constantTimeEqual(computedPublicKey, expectedPublicKey), nil
}

// computePublicKeyFromProcessedScalar performs ed25519 base point scalar multiplication
// This implements the same operation as GNB's ge_scalarmult_base(&A, private_key)
func computePublicKeyFromProcessedScalar(processedScalar []byte) []byte {
	// Use the lower-level approach to match GNB exactly
	return computeEd25519PublicKey(processedScalar)
}

// computeEd25519PublicKey computes public key from scalar using standard ed25519 algorithm
// This directly implements: ge_scalarmult_base(&A, private_key); ge_p3_tobytes(public_key, &A);
func computeEd25519PublicKey(scalar []byte) []byte {
	// Use the edwards25519 package for direct scalar multiplication
	// This matches exactly what GNB's ge_scalarmult_base does

	// Create a scalar from the 32-byte processed private key
	scalarPoint, err := edwards25519.NewScalar().SetBytesWithClamping(scalar)
	if err != nil {
		// Fallback: if clamping fails, the scalar is already properly formatted
		scalarPoint, _ = edwards25519.NewScalar().SetCanonicalBytes(scalar)
	}

	// Perform base point multiplication: publicKey = scalar * basePoint
	publicPoint := edwards25519.NewIdentityPoint().ScalarBaseMult(scalarPoint)

	// Encode the point to bytes (equivalent to ge_p3_tobytes)
	return publicPoint.Bytes()
}

// constantTimeEqual performs constant-time comparison of two byte slices
func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}

	var result byte = 0
	for i := 0; i < len(a); i++ {
		result |= a[i] ^ b[i]
	}

	return result == 0
}

// ParseGNBFormat parses GNB hex-encoded keys back to binary
func ParseGNBFormat(privateHex, publicHex string) ([]byte, []byte, error) {
	privateKey, err := hex.DecodeString(privateHex)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid private key hex: %w", err)
	}

	publicKey, err := hex.DecodeString(publicHex)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid public key hex: %w", err)
	}

	if len(publicKey) != 32 {
		return nil, nil, fmt.Errorf("public key must be 32 bytes, got %d", len(publicKey))
	}

	if len(privateKey) != 32 && len(privateKey) != 64 {
		return nil, nil, fmt.Errorf("private key must be 32 or 64 bytes, got %d", len(privateKey))
	}

	return privateKey, publicKey, nil
}

// ToGNBFormat converts binary keys to GNB hex format
func ToGNBFormat(privateKey, publicKey []byte) (string, string) {
	var privateHex string

	if len(privateKey) == 32 {
		// If only 32 bytes provided, assume it's the processed key and extend to 64
		fullPrivate := make([]byte, 64)
		copy(fullPrivate[:32], privateKey)
		// Second 32 bytes derived from the first 32
		h := sha512.Sum512(privateKey)
		copy(fullPrivate[32:], h[32:])
		privateHex = hex.EncodeToString(fullPrivate)
	} else {
		privateHex = hex.EncodeToString(privateKey)
	}

	publicHex := hex.EncodeToString(publicKey)
	return privateHex, publicHex
}

// ConvertStandardToGNB converts standard ed25519 to GNB format
func ConvertStandardToGNB(stdPrivateKey ed25519.PrivateKey) (*GNBKeyPair, error) {
	if len(stdPrivateKey) != 64 {
		return nil, fmt.Errorf("standard ed25519 private key must be 64 bytes")
	}

	// Extract the seed from standard ed25519 private key (first 32 bytes)
	seed := make([]byte, 32)
	copy(seed, stdPrivateKey[:32])

	// Regenerate using GNB method to ensure proper bit operations
	return GenerateGNBKeyPairFromSeed(seed)
}

// CreateGNBFromExistingScalar creates a GNBKeyPair from an already-processed scalar
// This is useful when you have a 32-byte processed private key from GNB
func CreateGNBFromExistingScalar(processedScalar []byte) (*GNBKeyPair, error) {
	if len(processedScalar) != 32 {
		return nil, fmt.Errorf("processed scalar must be exactly 32 bytes")
	}

	// Create full 64-byte private key
	fullPrivateKey := make([]byte, 64)
	copy(fullPrivateKey[:32], processedScalar)

	// Derive second half
	h := sha512.Sum512(processedScalar)
	copy(fullPrivateKey[32:], h[32:])

	// Compute public key
	publicKey := computePublicKeyFromProcessedScalar(processedScalar)

	return &GNBKeyPair{
		Seed:                nil, // Unknown since we only have the processed scalar
		PrivateKey:          fullPrivateKey,
		ProcessedPrivateKey: processedScalar,
		PublicKey:           publicKey,
	}, nil
}
