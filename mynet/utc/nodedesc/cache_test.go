package nodedesc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func cacheDescriptor() Descriptor {
	descriptor := goldenDescriptor()
	descriptor.NotBefore = 1000
	descriptor.ValidUntil = 10000
	return descriptor
}

func cacheExpect() Expect {
	expect := goldenExpect()
	expect.NowMS = 5000
	expect.MaxSkewMS = 100
	expect.MinValidUntil = 0
	return expect
}

func serveFrame(t *testing.T, frame []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(frame)
	}))
}

func TestResolverCachesAndFallsBack(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	frame := signForTest(t, privateKey, cacheDescriptor())
	expect := cacheExpect()
	cache := filepath.Join(t.TempDir(), "node.cache")

	server := serveFrame(t, frame)
	resolver := Resolver{URL: server.URL, CachePath: cache, MaxStaleMS: 2000}
	descriptor, source, err := resolver.Resolve(context.Background(), server.Client(),
		expect, publicKey, 5000)
	if err != nil || source != SourceNetwork || descriptor.ValidUntil != 10000 {
		t.Fatalf("network resolve = %+v, %v, %v", descriptor, source, err)
	}
	info, err := os.Stat(cache)
	if err != nil || info.Size() != cacheFileLen || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache stat = %+v, %v", info, err)
	}
	server.Close()

	descriptor, source, err = resolver.Resolve(context.Background(), http.DefaultClient,
		expect, publicKey, 6000)
	if err != nil || source != SourceCache || descriptor.ValidUntil != 10000 {
		t.Fatalf("cache resolve = %+v, %v, %v", descriptor, source, err)
	}

	if _, _, err := resolver.Resolve(context.Background(), http.DefaultClient,
		expect, publicKey, 8000); err == nil {
		t.Fatal("cache older than MaxStaleMS was accepted")
	}
	if _, _, err := resolver.Resolve(context.Background(), http.DefaultClient,
		expect, publicKey, 20000); err == nil {
		t.Fatal("expired cache descriptor was accepted")
	}
}

func TestResolverRejectsRollbackAndTamper(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	expect := cacheExpect()
	cache := filepath.Join(t.TempDir(), "node.cache")

	newer := cacheDescriptor()
	newer.ValidUntil = 20000
	newerFrame := signForTest(t, privateKey, newer)
	server := serveFrame(t, newerFrame)
	resolver := Resolver{URL: server.URL, CachePath: cache}
	if _, _, err := resolver.Resolve(context.Background(), server.Client(),
		expect, publicKey, 5000); err != nil {
		t.Fatalf("prime cache: %v", err)
	}
	before, err := os.ReadFile(cache)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	server.Close()

	olderFrame := signForTest(t, privateKey, cacheDescriptor())
	older := serveFrame(t, olderFrame)
	resolver.URL = older.URL
	if _, _, err := resolver.Resolve(context.Background(), older.Client(),
		expect, publicKey, 5000); err == nil {
		t.Fatal("gateway rollback was accepted")
	}
	after, err := os.ReadFile(cache)
	if err != nil || string(before) != string(after) {
		t.Fatal("rollback attempt changed the cache")
	}
	older.Close()

	tampered := append([]byte{}, newerFrame...)
	tampered[20] ^= 1
	if err := writeCache(cache, tampered, 5000); err != nil {
		t.Fatalf("write tampered cache: %v", err)
	}
	if _, _, err := resolver.Resolve(context.Background(), older.Client(),
		expect, publicKey, 5000); err == nil {
		t.Fatal("tampered cache was accepted")
	}
	if err := os.Chmod(cache, 0o644); err != nil {
		t.Fatalf("chmod cache: %v", err)
	}
	if _, _, err := resolver.Resolve(context.Background(), older.Client(),
		expect, publicKey, 5000); err == nil {
		t.Fatal("group/world readable cache was accepted")
	}
}

func TestResolverNeverFallsBackToUnsigned(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	expect := cacheExpect()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	resolver := Resolver{URL: server.URL,
		CachePath: filepath.Join(t.TempDir(), "missing.cache")}
	if _, _, err := resolver.Resolve(context.Background(), server.Client(),
		expect, publicKey, 5000); err == nil {
		t.Fatal("missing cache and failed gateway returned success")
	}
}

func TestFetchFrameHTTPReturnsVerifiedBytes(t *testing.T) {
	frame := goldenFrame(t)
	server := serveFrame(t, frame)
	defer server.Close()
	got, descriptor, err := FetchFrameHTTP(context.Background(), server.Client(),
		server.URL, goldenExpect(), goldenPublicKey(t))
	if err != nil || string(got) != string(frame) || descriptor != goldenDescriptor() {
		t.Fatalf("FetchFrameHTTP = %d bytes, %+v, %v", len(got), descriptor, err)
	}
}
