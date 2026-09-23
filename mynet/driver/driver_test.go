package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

func TestWGCapsNoDiscoveryNoProxy(t *testing.T) {
	d := NewWireGuardDriver("wg0", 1)
	c := d.Capabilities()
	if c.Discovery || c.Proxy {
		t.Fatalf("wg must not have Discovery/Proxy: %+v", c)
	}
	if c.HotReload {
		t.Fatal("HotReload 未验证必须 false")
	}
}

func TestGNBCapsHaveDiscoveryProxy(t *testing.T) {
	d := NewGNBDriver(1)
	c := d.Capabilities()
	if !c.Discovery || !c.Proxy {
		t.Fatalf("gnb must have Discovery/Proxy: %+v", c)
	}
	if c.HotReload {
		t.Fatal("HotReload 未验证必须 false")
	}
}

func TestParseWGDump(t *testing.T) {
	// 接口行 + 一行 peer
	dump := strings.Join([]string{
		"privkey\tpubkey\t51820\toff",
		"peerpub\t\taa.bb:51820\t10.20.0.2/32\t1700000000\t100\t200\t",
	}, "\n")
	rows := parseWGDump(dump)
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].isPeer || rows[0].listenPort != 51820 {
		t.Fatalf("iface row: %+v", rows[0])
	}
	if !rows[1].isPeer || rows[1].rx != 100 || rows[1].tx != 200 {
		t.Fatalf("peer row: %+v", rows[1])
	}
}

func TestRenderWGConfigFromIntent(t *testing.T) {
	psk := "preshared="
	ep := "203.0.113.2:51820"
	w := &domain.WireGuardIntent{
		Interface: domain.WgInterface{
			PrivateKey: "PRIV", PublicKey: "PUB", KeyAlgorithm: "curve25519", ListenPort: 51820,
		},
		Peers: []domain.WgPeer{{
			PublicKey: "PEERPUB", PresharedKey: &psk, BootstrapEndpoint: &ep,
			AllowedIPs: []string{"10.20.0.2/32"}, PersistentKeepaliveSecs: 25,
		}},
	}
	text := RenderWGConfig(w)
	for _, want := range []string{
		"PrivateKey = PRIV", "ListenPort = 51820",
		"[Peer]", "PublicKey = PEERPUB", "PresharedKey = preshared=",
		"Endpoint = 203.0.113.2:51820", "AllowedIPs = 10.20.0.2/32",
		"PersistentKeepalive = 25",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestWriteIntent0600(t *testing.T) {
	dir := t.TempDir()
	d := NewWireGuardDriver("wg0", 1)
	d.ConfPath = filepath.Join(dir, "wg0.conf")
	err := d.WriteIntent(&domain.WireGuardIntent{
		Interface: domain.WgInterface{PrivateKey: "K", PublicKey: "P", KeyAlgorithm: "curve25519"},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(d.ConfPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm=%v want 0600", st.Mode().Perm())
	}
}

func TestFactoryCreatesLifecycle(t *testing.T) {
	f := NewFactory()
	for _, typ := range []domain.VPNDriverType{domain.DriverTypeWireGuard, domain.DriverTypeGNB} {
		d, err := f.Create(typ, 42)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := AsLifecycle(d); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	if _, err := f.Create("openvpn", 1); err == nil {
		t.Fatal("unknown type must fail")
	}
}

func TestParseGNBStatusPong(t *testing.T) {
	peers := parseGNBStatus("peer 1 PONG\npeer 2 down\n")
	if len(peers) != 2 {
		t.Fatalf("len=%d", len(peers))
	}
	if !peers[0].IsOnline() || peers[1].IsOnline() {
		t.Fatalf("%+v", peers)
	}
}

func TestRenderGNBNodeConf(t *testing.T) {
	g := &domain.GnbIntent{
		Identity:  domain.GnbIdentity{NodeID: "1001", KeyAlgorithm: "ed25519"},
		Listeners: []string{"0.0.0.0:9000"},
	}
	text := RenderGNBNodeConf(g)
	if !strings.Contains(text, "node 1001") || !strings.Contains(text, "daemon off") {
		t.Fatalf("text=%s", text)
	}
}
