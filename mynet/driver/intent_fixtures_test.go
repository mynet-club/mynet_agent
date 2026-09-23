package driver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	candidates := []string{
		filepath.Join("..", "..", "mynet_ctl", "docs", "knowledge-base", "spec", "fixtures", "bundle-envelope-v2", name),
		filepath.Join("..", "..", "..", "mynet_ctl", "docs", "knowledge-base", "spec", "fixtures", "bundle-envelope-v2", name),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	t.Skip("fixtures not found")
	return ""
}

// A-5：wireguard fixture 驱动 —— 解码 → 渲染 → 字段正确
func TestWireGuardMinimalFixture(t *testing.T) {
	raw, err := os.ReadFile(fixturePath(t, "wireguard-minimal.json"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := domain.DecodeBundleEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if env.Driver != domain.DriverTypeWireGuard {
		t.Fatalf("driver=%s", env.Driver)
	}
	// Envelope 互斥：wg 不携带 gnb intent
	if env.Gnb != nil {
		t.Fatal("driver=wireguard must not carry gnb intent")
	}
	if env.WireGuard == nil {
		t.Fatal("missing wireguard intent")
	}
	if env.WireGuard.Interface.KeyAlgorithm != "curve25519" {
		t.Fatalf("alg=%s", env.WireGuard.Interface.KeyAlgorithm)
	}
	text := RenderWGConfig(env.WireGuard)
	if !strings.HasPrefix(text, "[Interface]") {
		t.Fatalf("render=%q", text)
	}
}

func TestWireGuardMultiPeerFixture(t *testing.T) {
	raw, err := os.ReadFile(fixturePath(t, "wireguard-multi-peer.json"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := domain.DecodeBundleEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.WireGuard.Peers) < 2 {
		t.Fatalf("peers=%d", len(env.WireGuard.Peers))
	}
	var out []byte
	out, _ = json.Marshal(env.WireGuard)
	if len(out) == 0 {
		t.Fatal("empty")
	}
}

func TestGnbMinimalFixtureNoWireGuard(t *testing.T) {
	raw, err := os.ReadFile(fixturePath(t, "gnb-minimal.json"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := domain.DecodeBundleEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if env.WireGuard != nil {
		t.Fatal("driver=gnb must not carry wireguard intent")
	}
	if env.Gnb == nil {
		t.Fatal("missing gnb intent")
	}
}
