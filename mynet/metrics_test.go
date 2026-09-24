package mynet

import (
	"encoding/json"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent-V2/shared/domain"
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
func TestDarwinSystemMetricsMatchSysctl(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin 专用：本机是 " + runtime.GOOS + "，sysctl/vm_stat/top 的口径不同")
	}
	m := &Metrics{}
	collectDarwinSystemMetrics(m)

	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		t.Fatalf("sysctl hw.memsize: %v", err)
	}
	sz, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Fatalf("解析 hw.memsize: %v", err)
	}
	if want := sz / 1024 / 1024; uint64(m.System.MemTotalMB) != want {
		t.Errorf("MemTotalMB = %d，而 sysctl hw.memsize 说 %d", m.System.MemTotalMB, want)
	}
	if m.System.MemUsedMB == 0 || m.System.MemUsedMB > m.System.MemTotalMB {
		t.Errorf("MemUsedMB = %d 不合理（应在 0 与总量 %d 之间）", m.System.MemUsedMB, m.System.MemTotalMB)
	}
	if m.System.UptimeSec <= 0 {
		t.Errorf("UptimeSec = %d，期望 > 0", m.System.UptimeSec)
	}
	if m.System.Load1 < 0 {
		t.Errorf("Load1 = %v 为负", m.System.Load1)
	}
	// CPUPercent 与 Load1 都**允许**为 0（机器真的空闲时就是 0），
	// 所以不断言非 0 —— 断言一个可能合法为 0 的值非 0，是会让测试随机红的。
}

// vm_stat 的页大小必须被读到，否则 used 会算成 0
// （拿不到页大小时我们选择报 0，而不是瞎猜 4096 —— 本机实测是 16384）。
func TestDarwinMemUsedNonZero(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin 专用")
	}
	if v := darwinMemUsedMB(); v == 0 {
		t.Fatal("darwinMemUsedMB() = 0 —— 页大小没读到，或 vm_stat 输出格式变了")
	}
}

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
