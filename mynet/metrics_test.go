package mynet

import (
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

func TestMetricsJSONShape(t *testing.T) {
	m := &Metrics{}
	collectSystemMetrics(m)
	m.VPN = append(m.VPN, VPNDriverMetrics{Driver: "gnb", Up: true, Peers: 2})
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{`"driver"`, `"gnb"`, `"system"`} {
		if !strings.Contains(s, key) {
			t.Fatalf("missing %s in %s", key, s)
		}
	}
	// 业务/心跳字段不得含驱动原生术语
	for _, bad := range []string{"AllowedIPs", "node.conf", "wg0", "gnb_tun"} {
		if strings.Contains(s, bad) {
			t.Fatalf("metrics leaked driver term %s: %s", bad, s)
		}
	}
}

// ── V4-1：指标真赋值的验证 ─────────────────────────────────
//
// 改之前 `collectDarwinSystemMetrics` 是空壳（读一个不存在的
// /var/run/system_info 然后 `_ = b` 丢掉），macOS 上系统段恒为全 0；
// `probeWireGuard`/`probeGNB` 的解析循环体是 `_ = fields`，四个 VPN 字段
// 从来没被赋过值。下面这些测试就是钉住「不许再退回全 0」。

// countUpInterfaces 在任何能跑 go test 的机器上都该 > 0 —— 否则它连不上网、
// 也跑不了测试。所以这条在 linux / darwin 都成立，不需要跳过。
func TestCountUpInterfacesNonZero(t *testing.T) {
	if n := countUpInterfaces(); n <= 0 {
		t.Fatalf("countUpInterfaces() = %d，期望 > 0（loopback 已排除，但总该有物理/虚拟网卡）", n)
	}
}

// darwin 的系统指标必须拿到**真值**，并与 sysctl 交叉核对 ——
// 只断言「非 0」不够，那只能证明有东西，不能证明是对的。

// vm_stat 的页大小必须被读到，否则 used 会算成 0
// （拿不到页大小时我们选择报 0，而不是瞎猜 4096 —— 本机实测是 16384）。

// ── 解析辅助（纯函数，跨平台）──────────────────────────────

func TestUintAfterColon(t *testing.T) {
	// 用 vm_stat 的真实行形状：大量对齐空格 + 尾随句点
	if got := uintAfterColon("Pages active:                                 257583."); got != 257583 {
		t.Errorf("= %d，期望 257583", got)
	}
	if got := uintAfterColon("Pages occupied by compressor:                 333637."); got != 333637 {
		t.Errorf("= %d，期望 333637", got)
	}
	if got := uintAfterColon("no colon here"); got != 0 {
		t.Errorf("无冒号应返回 0，得到 %d", got)
	}
}

func TestLeadingUint(t *testing.T) {
	if got := leadingUint("16384 bytes)"); got != 16384 {
		t.Errorf("= %d，期望 16384", got)
	}
	if got := leadingUint("page size"); got != 0 {
		t.Errorf("开头非数字应返回 0，得到 %d", got)
	}
}

// ── projectPeers：本机不算 peer、地址去重、relay 不算 up、nil 不 panic ──

func TestProjectPeersSkipsLocalDedupsAddrs(t *testing.T) {
	peers := []*domain.VPNPeer{
		{NodeID: 1, ConnectionStatus: "online", ConnectionType: "direct_ipv4", VirtualIP: "10.1.0.1", VirtualIP6: "2001:db8::1"},
		{NodeID: 2, ConnectionStatus: "relay", VirtualIP: "10.1.0.2"},
		{NodeID: 3, IsLocal: true, VirtualIP: "10.1.0.3"},
		{NodeID: 4, ConnectionStatus: "online", ConnectionType: "direct_ipv4", VirtualIP: "10.1.0.1"}, // 与 peer 1 同地址
		nil,
	}
	m := projectPeers("gnb", "", peers)
	if m.Peers != 3 {
		t.Errorf("Peers = %d，期望 3（本机不算 peer，否则恒多 1）", m.Peers)
	}
	if m.PeersUp != 2 {
		t.Errorf("PeersUp = %d，期望 2（relay 连得上但不是直连，不算 up）", m.PeersUp)
	}
	if len(m.Addrs) != 3 {
		t.Errorf("Addrs = %v，期望 3 个（10.1.0.1 去重、10.1.0.2、2001:db8::1）", m.Addrs)
	}
	// 心跳逐对端：online/relay 都要进，本地与 nil 不进。
	if len(m.PeersStatus) != 3 {
		t.Fatalf("PeersStatus = %+v，期望 3 条（online 2 + relay 1，本机/nil 跳过）", m.PeersStatus)
	}
	if m.PeersStatus[0].PeerID != 1 || m.PeersStatus[0].ConnType != "direct_ipv4" {
		t.Errorf("peer1 status = %+v", m.PeersStatus[0])
	}
	if m.PeersStatus[1].PeerID != 2 || m.PeersStatus[1].ConnType != "relay" {
		t.Errorf("peer2 status = %+v", m.PeersStatus[1])
	}
	if m.Driver != "gnb" || !m.Up {
		t.Errorf("Driver/Up 不对: %+v", m)
	}
}

// ── resolveLiveGNBIface：配置口名 ≠ 真实口名时按 overlay IP 反查 ──
//
// darwin/gnb 把 ifname 覆盖成 utunN（gnb_drv_darwin.c:249-252），配置里的
// gnb_tun 永远查不到。此前 detectGNBInterface 只查配置名 → probeGNB 报
// up:false → 心跳 status=down → 后端标离线、GUI 设备页无状态（2026-10-15）。
// 这里锁住回退行为，用 loopback 做真实口（任何跑测试的机器都有）。

func loopbackName(t *testing.T) string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("net.Interfaces: %v", err)
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ipnet.IP.IsLoopback() {
				return iface.Name
			}
		}
	}
	t.Skip("无 loopback 接口")
	return ""
}

func TestResolveLiveGNBIfaceConfiguredNameWins(t *testing.T) {
	name := loopbackName(t)
	got := resolveLiveGNBIface(name, "203.0.113.1")
	if got != name {
		t.Fatalf("配置口名在场且 UP 时应直接用：got %q, want %q", got, name)
	}
}

func TestResolveLiveGNBIfaceFallsBackToOverlayIP(t *testing.T) {
	name := loopbackName(t)
	// 配置名是一个不可能存在的口 —— darwin gnb_tun 的同形场景
	got := resolveLiveGNBIface("gnb_tun_zzz_no_such_if", "127.0.0.1")
	if got != name {
		t.Fatalf("配置名不在时应按 overlay IP 反查真实口：got %q, want %q", got, name)
	}
}

func TestResolveLiveGNBIfaceMissingBoth(t *testing.T) {
	if got := resolveLiveGNBIface("gnb_tun_zzz_no_such_if", "203.0.113.1"); got != "" {
		t.Fatalf("配置名与 overlay IP 都对不上时应返回空（宁缺不猜），got %q", got)
	}
	if got := resolveLiveGNBIface("", "203.0.113.1"); got != "" {
		t.Fatalf("配置名为空且 IP 对不上时应返回空，got %q", got)
	}
}
