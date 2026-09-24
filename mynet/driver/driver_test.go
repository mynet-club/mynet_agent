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

// gnbStatusFixture 按上游源码 `mynet_gnb/src/src/ctl/gnb_ctl_dump.c:38-180`
// 的 printf 逐行推导：字段名、顺序、分隔符都对齐源码。
//
// ⚠️ 这**不是**从真机抓的输出 —— `gnb_ctl` 是 Linux 二进制（开发机 macOS 上
// `exec format error`），且需要连到运行中的 gnb_es 共享内存。所以本测试属
// 「按源码推导的夹具」，不是真机验证；V4-1 因此记为 V4-1a。
// 但它比早先那个 `"peer 1 PONG\npeer 2 down\n"` 强得多 —— 那个格式在 gnb 里
// 根本不存在，等于测了个空气。
const gnbStatusFixture = `conf->conf_dir[/etc/gnb/conf]
node_num[3]
wan6_port[7676]
wan4_port[7676]

====================
node 1000000000000001
addr4_ping_latency_usec 1500
tun_ipv6 2001:db8::1
tun_ipv4 10.1.0.1
in  4096 (4.000K bytes)
out 2048 (2.000K bytes)
public_key aaaa1111
ipv6 Direct Point to Point
ipv4 Direct Point to Point
ping_ts_sec:1699999999(2026-09-24 10:00:00)
addr6_ping_latency_usec:1400
addr4_ping_latency_usec:1500
detect_count 5
wan_ipv6 [2001:db8::9]:7676
wan_ipv4 1.2.3.4:7676
available_address6:
available_address4:

====================
node 1000000000000002
addr4_ping_latency_usec 0
tun_ipv6 2001:db8::2
tun_ipv4 10.1.0.2
in  0 (0 bytes)
out 0 (0 bytes)
public_key bbbb2222
ipv6 InDirect
ipv4 InDirect
ping_ts_sec:0(1970-01-01 08:00:00)
detect_count 0

====================
node 1000000000000003
addr4_ping_latency_usec 0
tun_ipv6 2001:db8::3
tun_ipv4 10.1.0.3
in  0 (0 bytes)
out 0 (0 bytes)
public_key cccc3333
ipv6 Local node
ipv4 Local node
ping_ts_sec:0(1970-01-01 08:00:00)
detect_count 0
`

func TestParseGNBStatusRealFormat(t *testing.T) {
	peers := parseGNBStatus(gnbStatusFixture)

	// 核心回归：早先的实现把**每一行**都当成一个 peer。
	// 这个夹具的非空行数是 40+，所以「== 3」这一条同时锁住了那个 bug。
	if len(peers) != 3 {
		t.Fatalf("peer 数 = %d，期望 3（与 node_num[3] 一致）；若远大于 3 说明又按行计数了", len(peers))
	}
	nonEmpty := 0
	for _, l := range strings.Split(gnbStatusFixture, "\n") {
		if strings.TrimSpace(l) != "" {
			nonEmpty++
		}
	}
	if len(peers) == nonEmpty {
		t.Fatalf("peer 数等于非空行数(%d) —— 正是旧实现的错误行为", nonEmpty)
	}

	direct := peers[0]
	if direct.NodeID != 1000000000000001 || direct.PeerID != 1000000000000001 {
		t.Fatalf("node id 解析错: %+v", direct)
	}
	if !direct.IsOnline() {
		t.Fatalf("v4+v6 都 Direct 应为 online: %+v", direct)
	}
	if direct.ConnectionType != "dual_stack" {
		t.Fatalf("v4+v6 双直连应为 dual_stack，得到 %q", direct.ConnectionType)
	}
	if direct.VirtualIP != "10.1.0.1" || direct.VirtualIP6 != "2001:db8::1" {
		t.Fatalf("tun 地址解析错: %q / %q", direct.VirtualIP, direct.VirtualIP6)
	}
	if direct.RxBytes != 4096 || direct.TxBytes != 2048 {
		t.Fatalf("in/out 字节解析错: rx=%d tx=%d", direct.RxBytes, direct.TxBytes)
	}
	if direct.LatencyMs != 1.5 {
		t.Fatalf("延迟应为 1500usec → 1.5ms，得到 %v", direct.LatencyMs)
	}
	if direct.IsLocal {
		t.Fatal("第一个节点不该被判为本机")
	}

	relay := peers[1]
	if relay.ConnectionStatus != "relay" {
		t.Fatalf("InDirect 应为 relay，得到 %q", relay.ConnectionStatus)
	}
	if relay.IsOnline() {
		t.Fatal("relay 不该算 online")
	}

	local := peers[2]
	if !local.IsLocal {
		t.Fatalf("Local node 未标记 IsLocal: %+v", local)
	}
}

// 头部行（conf_dir / node_num / wan*_port）不属于任何 peer，
// 且在没有 peer 段时不该产出任何东西。
func TestParseGNBStatusHeaderOnly(t *testing.T) {
	peers := parseGNBStatus("conf->conf_dir[/x]\nnode_num[0]\nwan6_port[7676]\nwan4_port[7676]\n")
	if len(peers) != 0 {
		t.Fatalf("只有头部时应无 peer，得到 %d 个", len(peers))
	}
}

// 空输出 / 命令失败的空串不该 panic，也不该产出 peer。
func TestParseGNBStatusEmpty(t *testing.T) {
	if p := parseGNBStatus(""); len(p) != 0 {
		t.Fatalf("空输出应无 peer，得到 %d", len(p))
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
