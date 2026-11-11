package nodedesc

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Source reports where a resolved NodeDescriptor came from.
type Source int

const (
	SourceNetwork Source = iota + 1
	SourceCache
)

// Resolver fetches a NodeDescriptor through the Gateway and keeps the last
// verified frame as a bounded fallback.
//
// The Gateway is delivery-only and may be unavailable; a cached descriptor is
// used only after it passes the same Verify against the caller's trust context
// and is no older than MaxStaleMS. There is no unsigned fallback, no TOFU and
// no reuse of a descriptor whose validity window has expired. A cached frame
// with a newer valid_until than the network response is treated as a rollback,
// refused, and left untouched.
type Resolver struct {
	URL        string
	CachePath  string
	MaxStaleMS uint64
}

const (
	cacheMagic     = "UTCNCAC1"
	cacheHeaderLen = 16
	cacheFileLen   = cacheHeaderLen + frameBytes
)

// Resolve tries the Gateway first, then the cache. nowMS is the verifier
// clock; it is also recorded as the cache acceptance time.
func (r Resolver) Resolve(ctx context.Context, client *http.Client,
	expect Expect, publicKey ed25519.PublicKey, nowMS uint64) (Descriptor, Source, error) {
	var zero Descriptor

	if r.URL == "" || r.CachePath == "" || len(publicKey) != ed25519.PublicKeySize {
		return zero, 0, errors.New("nodedesc: resolver needs URL, cache path and key")
	}
	fetchExpect := expect
	fetchExpect.NowMS = nowMS
	frame, descriptor, fetchErr := FetchFrameHTTP(ctx, client, r.URL, fetchExpect, publicKey)
	if fetchErr == nil {
		if cached, _, cacheErr := loadCache(r.CachePath, fetchExpect, publicKey); cacheErr == nil &&
			cached.ValidUntil > descriptor.ValidUntil {
			return zero, 0, fmt.Errorf(
				"nodedesc: gateway rollback: cached valid_until %d > %d",
				cached.ValidUntil, descriptor.ValidUntil)
		}
		if err := writeCache(r.CachePath, frame, nowMS); err != nil {
			return zero, 0, err
		}
		return descriptor, SourceNetwork, nil
	}

	cached, fetchedAt, cacheErr := loadCache(r.CachePath, fetchExpect, publicKey)
	if cacheErr != nil {
		return zero, 0, fmt.Errorf("nodedesc: gateway unavailable (%v) and cache unusable (%v)",
			fetchErr, cacheErr)
	}
	if nowMS < fetchedAt {
		return zero, 0, errors.New("nodedesc: cache timestamp is in the future")
	}
	if r.MaxStaleMS > 0 && nowMS-fetchedAt > r.MaxStaleMS {
		return zero, 0, fmt.Errorf("nodedesc: cache is older than %d ms", r.MaxStaleMS)
	}
	return cached, SourceCache, nil
}

func writeCache(path string, frame []byte, nowMS uint64) error {
	if len(frame) != frameBytes {
		return errors.New("nodedesc: cache frame has the wrong length")
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".nodedesc-*")
	if err != nil {
		return fmt.Errorf("nodedesc: cache: %w", err)
	}
	name := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(name)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("nodedesc: cache: %w", err)
	}
	header := make([]byte, cacheHeaderLen)
	copy(header, cacheMagic)
	binary.BigEndian.PutUint64(header[8:], nowMS)
	if _, err := temporary.Write(header); err != nil {
		cleanup()
		return fmt.Errorf("nodedesc: cache: %w", err)
	}
	if _, err := temporary.Write(frame); err != nil {
		cleanup()
		return fmt.Errorf("nodedesc: cache: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("nodedesc: cache: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("nodedesc: cache: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("nodedesc: cache: %w", err)
	}
	return nil
}

func loadCache(path string, expect Expect,
	publicKey ed25519.PublicKey) (Descriptor, uint64, error) {
	var zero Descriptor
	info, err := os.Lstat(path)
	if err != nil {
		return zero, 0, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		info.Size() != cacheFileLen {
		return zero, 0, errors.New("nodedesc: cache must be a private 468-byte file")
	}
	file, err := os.Open(path)
	if err != nil {
		return zero, 0, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, cacheFileLen+1))
	if err != nil {
		return zero, 0, err
	}
	if len(raw) != cacheFileLen || string(raw[:8]) != cacheMagic {
		return zero, 0, errors.New("nodedesc: cache header is invalid")
	}
	fetchedAt := binary.BigEndian.Uint64(raw[8:16])
	if fetchedAt == 0 {
		return zero, 0, errors.New("nodedesc: cache timestamp is missing")
	}
	descriptor, result := Verify(raw[cacheHeaderLen:], expect, publicKey)
	if result != OK {
		return zero, 0, fmt.Errorf("nodedesc: cached frame: %s", ResultName(result))
	}
	return descriptor, fetchedAt, nil
}
