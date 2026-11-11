package hubd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

const nodeDomain = "mynet.utc.access.node-descriptor.v1"

func signedNodeDescriptor(t *testing.T, privateKey ed25519.PrivateKey,
	host string, port uint16, pin [32]byte) []byte {
	t.Helper()
	frame := make([]byte, 452)
	copy(frame[0:], []byte("UTCNODS1"))
	binary.BigEndian.PutUint16(frame[8:], 1)
	binary.BigEndian.PutUint16(frame[10:], 0)
	binary.BigEndian.PutUint32(frame[12:], 452)
	binary.BigEndian.PutUint16(frame[16:], port)
	slots := []string{"core.primary-1", "utc-core.1", "core-key-1", "core", host}
	for index, value := range slots {
		copy(frame[20+index*64:], []byte(value))
	}
	now := uint64(time.Now().UnixMilli())
	binary.BigEndian.PutUint64(frame[340:], now-60000)
	binary.BigEndian.PutUint64(frame[348:], now+600000)
	copy(frame[356:], pin[:])
	message := append([]byte(nodeDomain), frame[:388]...)
	copy(frame[388:], ed25519.Sign(privateKey, message))
	return frame
}

func TestBootstrapResolvesAndCachesNodeDescriptor(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var pin [32]byte
	for i := range pin {
		pin[i] = byte(0x50 + i)
	}
	frame := signedNodeDescriptor(t, privateKey, "10.230.2.10", 39444, pin)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(frame)
	}))
	defer server.Close()

	cache := filepath.Join(t.TempDir(), "node.cache")
	config := Config{Bootstrap: &BootstrapConfig{
		URL: server.URL, CachePath: cache, MaxStaleMS: 60000,
		DescriptorPublicKey: hex.EncodeToString(publicKey),
		IssuerID:            "utc-core.1", KeyID: "core-key-1", Scope: "core",
		Pin: hex.EncodeToString(pin[:]),
	}}
	if err := bootstrapWithClient(context.Background(), &config, server.Client()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if config.HubAddress != "10.230.2.10:39444" || config.Scope != "core" ||
		config.Identity.Scope != "core" {
		t.Fatalf("config = %+v", config)
	}

	server.Close()
	cached := Config{Bootstrap: config.Bootstrap}
	if err := bootstrapWithClient(context.Background(), &cached, http.DefaultClient); err != nil {
		t.Fatalf("cached bootstrap: %v", err)
	}
	if cached.HubAddress != "10.230.2.10:39444" {
		t.Fatalf("cached address = %q", cached.HubAddress)
	}

	bad := Config{Bootstrap: config.Bootstrap, HubAddress: "127.0.0.1:1"}
	if err := Bootstrap(context.Background(), &bad); err == nil {
		t.Fatal("static address and bootstrap were both accepted")
	}
}

func TestBootstrapRejectsWrongTrust(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var pin [32]byte
	for i := range pin {
		pin[i] = byte(0x70 + i)
	}
	frame := signedNodeDescriptor(t, privateKey, "10.0.0.5", 9443, pin)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(frame)
	}))
	defer server.Close()

	base := &BootstrapConfig{
		URL: server.URL, CachePath: filepath.Join(t.TempDir(), "node.cache"),
		DescriptorPublicKey: hex.EncodeToString(publicKey),
		IssuerID:            "utc-core.1", KeyID: "core-key-1", Scope: "core",
		Pin: hex.EncodeToString(pin[:]),
	}
	for name, mutate := range map[string]func(*BootstrapConfig){
		"issuer": func(b *BootstrapConfig) { b.IssuerID = "other" },
		"key":    func(b *BootstrapConfig) { b.KeyID = "other" },
		"scope":  func(b *BootstrapConfig) { b.Scope = "edge" },
		"pin":    func(b *BootstrapConfig) { b.Pin = hex.EncodeToString(make([]byte, 32)) },
	} {
		t.Run(name, func(t *testing.T) {
			copyConfig := *base
			mutate(&copyConfig)
			config := Config{Bootstrap: &copyConfig}
			if err := bootstrapWithClient(context.Background(), &config, server.Client()); err == nil {
				t.Fatal("wrong trust was accepted")
			}
		})
	}
}

func TestParseBootstrapRejectsIncomplete(t *testing.T) {
	if _, err := parseBootstrap([]byte(`{"url":"http://x"}`)); err == nil {
		t.Fatal("incomplete bootstrap config was accepted")
	}
	if _, err := parseBootstrap([]byte(`{"url":"http://x","unknown":1}`)); err == nil {
		t.Fatal("unknown bootstrap field was accepted")
	}
	raw := []byte(`{"url":"https://gw","cache_path":"/tmp/c","descriptor_public_key":"` +
		hex.EncodeToString(make([]byte, 32)) + `","descriptor_issuer":"i",` +
		`"descriptor_key":"k","descriptor_scope":"core","descriptor_pin":"` +
		hex.EncodeToString(make([]byte, 32)) + `"}`)
	if _, err := parseBootstrap(raw); err != nil {
		t.Fatalf("valid bootstrap: %v", err)
	}
}
