package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

func TestWGCapsNoDiscoveryNoProxy(t *testing.T) {
	d := NewWireGuardDriver("wg0", 1)
	c := d.Capabilities()
	if c.Discovery || c.Proxy {
		t.Fatalf("wg must not have Discovery/Proxy: %+v", c)
	}
	// HotReload：wg 的 Reload 走 `wg syncconf`（只同步 WG 协议状态、不断链路），
	// 不可用时回落 Stop+Start —— 已实现并验证（单测 4 例 + arm64 VM 真机 syncconf 热加对端）。
	if !c.HotReload {
		t.Fatal("wg 的热同步已落地（wg syncconf），HotReload 应为 true")
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
	text := RenderWGConfig(w, nil)
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
	// lastping：ping_ts_sec:1699999999 → LastSeen / Addr4UpdateTsSec
	if direct.LastSeen.Unix() != 1699999999 {
		t.Fatalf("lastping 应为 1699999999，得到 %v", direct.LastSeen)
	}
	if direct.Addr4UpdateTsSec != 1699999999 {
		t.Fatalf("Addr4UpdateTsSec=%d want 1699999999", direct.Addr4UpdateTsSec)
	}
	if direct.IsLocal {
		t.Fatal("第一个节点不该被判为本机")
	}

	relay := peers[1]
	if !relay.LastSeen.IsZero() {
		t.Fatalf("ping_ts_sec:0 不应产生 lastping，得到 %v", relay.LastSeen)
	}
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
	// 键名必须是 nodeid：gnb 用 strncmp(line,"nodeid",6) 匹配
	// （gnb_conf_file.c:293）。写成 `node 1001` 会比成 "node 1" vs "nodeid" →
	// 不匹配 → 整行静默跳过，本机 uuid 设不上。这个断言就是钉这一点。
	if !strings.Contains(text, "nodeid 1001") {
		t.Fatalf("缺 nodeid 键: text=%s", text)
	}
	if strings.Contains(text, "\nnode ") || strings.HasPrefix(text, "node ") {
		t.Fatalf("出现了 gnb 不认的 `node ` 键: text=%s", text)
	}
	if !strings.Contains(text, "listen 0.0.0.0:9000") {
		t.Fatalf("缺 listen: text=%s", text)
	}
	// daemon on：自己起时 gnb 双 fork 后父进程立即 exit(0)，Start() 才不阻塞；
	// 被 systemd 以 --systemd 拉起时该键被忽略（gnb_argv.c:439-440 +
	// gnb_conf_file.c:458），保持前台。daemon off 会让 Start() 永远挂住。
	if !strings.Contains(text, "daemon on") {
		t.Fatalf("应为 daemon on: text=%s", text)
	}
}

// 渲染器对 listen 的**形态**必须原样透传，因为 gnb 按形态决定绑哪个地址族：
//
//	check_listen_string()（gnb_conf.c:71-81）：纯数字返 0 / 含 ':' 返 4 / '[' 开头返 6
//	分发（gnb_conf_file.c:300-333）：0 → 同时设 [:::port] 与 0.0.0.0:port（双栈同端口）
//	                                  4 → 只设 IPv4，IPv6 留在出厂默认 9001
//	                                  6 → 只设 IPv6
//
// 后端一度下发 `0.0.0.0:9016`，网关 182 的实测后果就是 v4 在 9016、v6 在 9001
// （map 因此叫 /tmp/gnb.9001.map），而全网对它的 index/对端宣告都写 9016 ——
// IPv6 侧的发现与打洞全打在没人听的端口上。2026-10-01 起后端只发裸端口，
// 这条把「裸端口 → listen 9016（没有 host 前缀）」钉在 agent 侧。
func TestRenderGNBNodeConfListenFormBarePortDualStack(t *testing.T) {
	g := &domain.GnbIntent{
		Identity:  domain.GnbIdentity{NodeID: "1001", KeyAlgorithm: "ed25519"},
		Listeners: []string{"9180"},
	}
	text := RenderGNBNodeConf(g)

	if !strings.Contains(text, "\nlisten 9180\n") && !strings.HasPrefix(text, "listen 9180\n") {
		t.Fatalf("裸端口该原样落成 `listen 9180`，text=%s", text)
	}
	for _, bad := range []string{"0.0.0.0:9180", "[::]:9180"} {
		if strings.Contains(text, bad) {
			t.Fatalf("渲染器不该改写 listen 形态成 %q（改了就单方面放弃一个地址族）: text=%s", bad, text)
		}
	}

	// 空白项不该产出一行 `listen `（gnb sscanf 拿不到值 → 未定义行为）
	g.Listeners = []string{"", "  ", "9016"}
	trimmed := RenderGNBNodeConf(g)
	if strings.Contains(trimmed, "listen \n") || strings.Contains(trimmed, "listen  \n") {
		t.Fatalf("空 listen 项不该落成行: text=%s", trimmed)
	}
	if strings.Count(trimmed, "listen ") != 1 {
		t.Fatalf("应当只有一行 listen，text=%s", trimmed)
	}
}
