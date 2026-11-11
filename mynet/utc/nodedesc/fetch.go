package nodedesc

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// FetchHTTP loads one NodeDescriptor from a Gateway URL.
//
// The Gateway is a delivery channel only: this function verifies the Core
// signature, issuer/key/scope binding, SPKI pin, validity window and rollback
// floor exactly like Verify. It does not cache, retry, follow an unsigned
// fallback or trust the Gateway's word; every failure returns an error and a
// zero Descriptor. The response body must be exactly the 452-byte frame.
func FetchHTTP(ctx context.Context, client *http.Client, url string,
	expect Expect, publicKey ed25519.PublicKey) (Descriptor, error) {
	_, descriptor, err := FetchFrameHTTP(ctx, client, url, expect, publicKey)
	return descriptor, err
}

// FetchFrameHTTP is FetchHTTP plus the exact verified 452-byte frame, so a
// caller can cache and later re-verify the same bytes.
func FetchFrameHTTP(ctx context.Context, client *http.Client, url string,
	expect Expect, publicKey ed25519.PublicKey) ([]byte, Descriptor, error) {
	var zero Descriptor
	if client == nil || url == "" {
		return nil, zero, errors.New("nodedesc: nil client or empty URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, zero, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, zero, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, zero, fmt.Errorf("nodedesc: gateway status %d", response.StatusCode)
	}
	frame, err := io.ReadAll(io.LimitReader(response.Body, frameBytes+1))
	if err != nil {
		return nil, zero, err
	}
	if len(frame) != frameBytes {
		return nil, zero, fmt.Errorf("nodedesc: gateway returned %d bytes, want %d",
			len(frame), frameBytes)
	}
	descriptor, result := Verify(frame, expect, publicKey)
	if result != OK {
		return nil, zero, fmt.Errorf("nodedesc: %s", ResultName(result))
	}
	return frame, descriptor, nil
}
