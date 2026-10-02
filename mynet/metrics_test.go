package mynet

import (
	"encoding/json"
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
		{NodeID: 1, ConnectionStatus: "online", VirtualIP: "10.1.0.1", VirtualIP6: "2001:db8::1"},
		{NodeID: 2, ConnectionStatus: "relay", VirtualIP: "10.1.0.2"},
		{NodeID: 3, IsLocal: true, VirtualIP: "10.1.0.3"},
		{NodeID: 4, ConnectionStatus: "online", VirtualIP: "10.1.0.1"}, // 与 peer 1 同地址
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
	if m.Driver != "gnb" || !m.Up {
		t.Errorf("Driver/Up 不对: %+v", m)
	}
}
