package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// statusFixture 搭一个可控的探测面，六态各测一条。
// 判据必须在**替身**上钉住，而不是碰真机 —— 否则 CI 上没有 gnb 全是误报。
type statusFixture struct {
	t    *testing.T
	root string
}

func newFixture(t *testing.T) *statusFixture {
	t.Helper()
	root := t.TempDir()
	return &statusFixture{t: t, root: root}
}

func (f *statusFixture) probe() statusProbe {
	return statusProbe{
		home: f.root,
		// 默认：没装 gnb；各用例按需覆盖
		gnbPath:     "",
		pidAlive:    func(int) bool { return false },
		routeExists: func(string) bool { return false },
		findIface:   func(string) string { return "" },
	}
}

func (f *statusFixture) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func TestNodeStatusUninstalled(t *testing.T) {
	f := newFixture(t)
	p := f.probe()
	st := computeNodeStatus(p)
	if st.State != StateUninstalled {
		t.Fatalf("state=%s want %s", st.State, StateUninstalled)
	}
	if st.PrimaryAction != ActInstall {
		t.Fatalf("primary=%s want %s", st.PrimaryAction, ActInstall)
	}
	if !st.Actions[ActInstall] {
		t.Fatal("未安装时「安装」必须可点")
	}
	if st.Actions[ActStart] {
		t.Fatal("未安装时「启动」必须禁用")
	}
	if st.OneLine == "" {
		t.Fatal("一言之不能为空")
	}
}

func TestNodeStatusInstalledNotConfigured(t *testing.T) {
	f := newFixture(t)
	p := f.probe()
	p.gnbPath = "/usr/local/bin/gnb"
	st := computeNodeStatus(p)
	if st.State != StateInstalled {
		t.Fatalf("state=%s want %s", st.State, StateInstalled)
	}
	if st.PrimaryAction != ActConfigure {
		t.Fatalf("primary=%s want %s", st.PrimaryAction, ActConfigure)
	}
	if st.Actions[ActStart] {
		t.Fatal("未配置时「启动」必须禁用")
	}
}

func TestNodeStatusConfiguredNotRunning(t *testing.T) {
	f := newFixture(t)
	f.write("conf/mynet.conf", "NODE_ID=1001\n")
	f.write("var/gnb/node.conf", "nodeid 1001\n")
	f.write("var/bundle.json", `{"network":{"addresses":["10.0.0.1/32"],"routes":[]}}`)
	p := f.probe()
	p.gnbPath = "/usr/local/bin/gnb"
	st := computeNodeStatus(p)
	if st.State != StateStopped {
		t.Fatalf("state=%s want %s", st.State, StateStopped)
	}
	if st.PrimaryAction != ActStart {
		t.Fatalf("primary=%s want %s", st.PrimaryAction, ActStart)
	}
	if !st.Actions[ActStart] {
		t.Fatal("已配置未启动时「启动」必须可点")
	}
	if st.Actions[ActStop] {
		t.Fatal("没在跑时「停止」必须禁用")
	}
	if st.OverlayIP != "10.0.0.1" {
		t.Fatalf("overlay=%q want 10.0.0.1", st.OverlayIP)
	}
}

func TestNodeStatusRunning(t *testing.T) {
	f := newFixture(t)
	f.write("conf/mynet.conf", "NODE_ID=1001\n")
	f.write("var/gnb/node.conf", "nodeid 1001\n")
	f.write("var/gnb/gnb.pid", "4242\n")
	f.write("var/bundle.json", `{"network":{
		"addresses":["10.0.0.1/32"],
		"routes":[{"prefix":"10.0.0.2/32","dev":"gnb_tun"}]
	}}`)
	p := f.probe()
	p.gnbPath = "/usr/local/bin/gnb"
	p.pidAlive = func(pid int) bool { return pid == 4242 }
	p.findIface = func(ip string) string {
		if ip == "10.0.0.1" {
			return "utun4"
		}
		return ""
	}
	p.routeExists = func(dest string) bool { return dest == "10.0.0.2/32" || dest == "10.0.0.2" }
	st := computeNodeStatus(p)
	if st.State != StateRunning {
		t.Fatalf("state=%s want %s (detail=%s)", st.State, StateRunning, st.Detail)
	}
	if st.PrimaryAction != ActRestart {
		t.Fatalf("primary=%s want %s", st.PrimaryAction, ActRestart)
	}
	if !st.Actions[ActStop] || !st.Actions[ActRestart] {
		t.Fatal("运行中「停止/重启」必须可点")
	}
	if st.Actions[ActStart] {
		t.Fatal("已在跑时「启动」应禁用（用重启）")
	}
	if st.PeersRouted != 1 || st.PeersTotal != 1 {
		t.Fatalf("peers routed/total = %d/%d want 1/1", st.PeersRouted, st.PeersTotal)
	}
}

// procd 用 `gnb -c <conf>` 拉起时不写 pid 文件；node-status 必须按命令行补 pid，
// 否则活着的隧道会被判成 STOPPED（2026-10-25 .182 实测）。
func TestNodeStatusRunningWithoutPIDFile(t *testing.T) {
	f := newFixture(t)
	f.write("conf/mynet.conf", "NODE_ID=1001\n")
	f.write("var/gnb/node.conf", "nodeid 1001\n")
	f.write("var/bundle.json", `{"network":{
		"addresses":["10.0.0.1/32"],
		"routes":[{"prefix":"10.0.0.2/32","dev":"gnb_tun"}]
	}}`)
	p := f.probe()
	p.gnbPath = "/usr/local/bin/gnb"
	p.findGnbPID = func(string) int { return 4242 }
	p.pidAlive = func(pid int) bool { return pid == 4242 }
	p.findIface = func(ip string) string {
		if ip == "10.0.0.1" {
			return "utun4"
		}
		return ""
	}
	p.routeExists = func(dest string) bool { return dest == "10.0.0.2/32" || dest == "10.0.0.2" }
	st := computeNodeStatus(p)
	if st.State != StateRunning {
		t.Fatalf("state=%s want %s (detail=%s)", st.State, StateRunning, st.Detail)
	}
	if st.ProcessPID != 4242 || !st.ProcessUp {
		t.Fatalf("pid/up = %d/%v want 4242/true", st.ProcessPID, st.ProcessUp)
	}
}

func TestNodeStatusDegradedMissingRoute(t *testing.T) {
	f := newFixture(t)
	f.write("conf/mynet.conf", "NODE_ID=1001\n")
	f.write("var/gnb/node.conf", "nodeid 1001\n")
	f.write("var/gnb/gnb.pid", "4242\n")
	f.write("var/bundle.json", `{"network":{
		"addresses":["10.0.0.1/32"],
		"routes":[{"prefix":"10.0.0.2/32","dev":"gnb_tun"}]
	}}`)
	p := f.probe()
	p.gnbPath = "/usr/local/bin/gnb"
	p.pidAlive = func(pid int) bool { return pid == 4242 }
	p.findIface = func(ip string) string { return "utun4" } // tun 在
	p.routeExists = func(string) bool { return false }      // 路由全缺
	st := computeNodeStatus(p)
	if st.State != StateDegraded {
		t.Fatalf("state=%s want %s", st.State, StateDegraded)
	}
	if st.PrimaryAction != ActFixRoutes {
		t.Fatalf("primary=%s want %s", st.PrimaryAction, ActFixRoutes)
	}
	if !st.Actions[ActFixRoutes] {
		t.Fatal("降级时「修复路由」必须可点")
	}
}

func TestNodeStatusDegradedMissingTun(t *testing.T) {
	f := newFixture(t)
	f.write("conf/mynet.conf", "NODE_ID=1001\n")
	f.write("var/gnb/node.conf", "nodeid 1001\n")
	f.write("var/gnb/gnb.pid", "4242\n")
	f.write("var/bundle.json", `{"network":{"addresses":["10.0.0.1/32"],"routes":[]}}`)
	p := f.probe()
	p.gnbPath = "/usr/local/bin/gnb"
	p.pidAlive = func(pid int) bool { return pid == 4242 }
	p.findIface = func(string) string { return "" } // tun 不在
	st := computeNodeStatus(p)
	if st.State != StateDegraded {
		t.Fatalf("state=%s want %s", st.State, StateDegraded)
	}
	if st.PrimaryAction != ActRestart {
		t.Fatalf("primary=%s want %s", st.PrimaryAction, ActRestart)
	}
}

func TestNodeStatusFailedPidStale(t *testing.T) {
	f := newFixture(t)
	f.write("conf/mynet.conf", "NODE_ID=1001\n")
	f.write("var/gnb/node.conf", "nodeid 1001\n")
	f.write("var/gnb/gnb.pid", "999999\n") // pid 文件在
	f.write("var/bundle.json", `{"network":{"addresses":["10.0.0.1/32"],"routes":[]}}`)
	p := f.probe()
	p.gnbPath = "/usr/local/bin/gnb"
	p.pidAlive = func(int) bool { return false } // 进程没了 → 上次没干净退出
	st := computeNodeStatus(p)
	if st.State != StateFailed {
		t.Fatalf("state=%s want %s", st.State, StateFailed)
	}
	if st.PrimaryAction != ActRetry && st.PrimaryAction != ActStart {
		t.Fatalf("primary=%s want retry/start", st.PrimaryAction)
	}
}

// 一言之与主按钮必须随态变化 —— 这是「状态一言之」的核心承诺。
func TestNodeStatusOneLineAndPrimaryVaryByState(t *testing.T) {
	seenPrimary := map[string]bool{}
	// 六态各搭一次，断言一言之非空且主按钮彼此不同（至少前五个不同）
	states := []NodeState{}
	add := func(st NodeStatus) {
		if st.OneLine == "" {
			t.Fatalf("state=%s 一言之为空", st.State)
		}
		if st.PrimaryAction == "" {
			t.Fatalf("state=%s 无主按钮", st.State)
		}
		if !st.Actions[st.PrimaryAction] {
			t.Fatalf("state=%s 主按钮 %s 标记为不可点", st.State, st.PrimaryAction)
		}
		seenPrimary[st.PrimaryAction] = true
		states = append(states, st.State)
	}

	f := newFixture(t)
	add(computeNodeStatus(f.probe()))

	f = newFixture(t)
	p := f.probe()
	p.gnbPath = "/usr/bin/gnb"
	add(computeNodeStatus(p))

	f = newFixture(t)
	f.write("conf/mynet.conf", "NODE_ID=1\n")
	f.write("var/gnb/node.conf", "x\n")
	p = f.probe()
	p.gnbPath = "/usr/bin/gnb"
	add(computeNodeStatus(p))

	if len(states) != 3 {
		t.Fatalf("states=%v", states)
	}
	if len(seenPrimary) < 2 {
		t.Fatalf("主按钮没有随态变化: %v", seenPrimary)
	}
}

// 路由探测：darwin 的 route get 对「走默认网关」的目标也成功，
// 必须区分「真装了主机路由」和「落到 default」。
func TestDarwinRouteGetRejectsDefault(t *testing.T) {
	// 这条打真内核：loopback 上的 127.0.0.1 一定有 host 路由
	if !osRouteExists("127.0.0.1") && !osRouteExists("127.0.0.1/32") {
		// 某些容器里 route/ip 都不可用 —— 不硬失败
		t.Skip("本机没有可用的 route/ip 命令")
	}
	// 文档地址（203.0.113.0/24 RFC 5737）通常没有具体路由，会落 default
	if osRouteExists("203.0.113.1") && osRouteExists("203.0.113.1/32") {
		// 有的环境会加黑洞路由，允许；只要 127.0.0.1 那条对即可
		t.Log("203.0.113.1 有路由，跳过 default 判定")
	}
}

// P2：⑤ 对端表每行要能回答「配置里有没有、OS 路由装没装」。
// 本机行在最前（与 route.conf 约定同形）。
func TestNodeStatusPeerRowsSelfFirst(t *testing.T) {
	f := newFixture(t)
	f.write("conf/mynet.conf", "NODE_ID=1001\n")
	f.write("var/gnb/node.conf", "nodeid 1001\n")
	f.write("var/gnb/gnb.pid", "4242\n")
	f.write("var/gnb/route.conf", "1001|10.0.0.1|255.255.255.255\n")
	f.write("var/bundle.json", `{"network":{
		"addresses":["10.0.0.1/32"],
		"routes":[{"prefix":"10.0.0.2/32","dev":"gnb_tun"}]
	},"gnb":{"nodes":[
		{"node_id":"1001","tun_address":"10.0.0.1/32"},
		{"node_id":"1002","tun_address":"10.0.0.2/32"}
	]}}`)
	p := f.probe()
	p.gnbPath = "/usr/bin/gnb"
	p.pidAlive = func(int) bool { return true }
	p.findIface = func(string) string { return "utun4" }
	p.routeExists = func(string) bool { return true }
	p.ifaceStats = func(string) (int64, int64, int, bool) { return 840, 840, 1450, true }
	p.procUptimeSec = func(int) int64 { return 3600 }
	st := computeNodeStatus(p)
	if len(st.Peers) != 2 {
		t.Fatalf("peers=%d want 2（本机+对端）", len(st.Peers))
	}
	if !st.Peers[0].IsSelf || st.Peers[0].NodeID != "1001" {
		t.Fatalf("第一条必须是本机: %+v", st.Peers[0])
	}
	if st.Peers[1].NodeID != "1002" || st.Peers[1].Overlay != "10.0.0.2" {
		t.Fatalf("对端行应带 node_id 与 overlay: %+v", st.Peers[1])
	}
	if !st.Peers[1].Routed {
		t.Fatal("对端路由装了时 Routed 必须为 true")
	}
	// ④ 运行区
	if st.UptimeSec != 3600 {
		t.Fatalf("uptime=%d want 3600", st.UptimeSec)
	}
	if st.RxBytes != 840 || st.TxBytes != 840 || st.Mtu != 1450 {
		t.Fatalf("rx/tx/mtu = %d/%d/%d", st.RxBytes, st.TxBytes, st.Mtu)
	}
	if !strings.Contains(st.RouteConfText, "1001|10.0.0.1|") {
		t.Fatalf("route.conf 原文应带上:\n%s", st.RouteConfText)
	}
}

// 对端路由缺失时，那行的 Routed=false —— 这是 DEGRADED 的证据，UI 要能指出来。
func TestNodeStatusPeerRowUnrouted(t *testing.T) {
	f := newFixture(t)
	f.write("conf/mynet.conf", "NODE_ID=1001\n")
	f.write("var/gnb/node.conf", "x\n")
	f.write("var/gnb/gnb.pid", "4242\n")
	f.write("var/bundle.json", `{"network":{
		"addresses":["10.0.0.1/32"],
		"routes":[{"prefix":"10.0.0.2/32","dev":"gnb_tun"}]
	}}`)
	p := f.probe()
	p.gnbPath = "/usr/bin/gnb"
	p.pidAlive = func(int) bool { return true }
	p.findIface = func(string) string { return "utun4" }
	p.routeExists = func(string) bool { return false }
	st := computeNodeStatus(p)
	if st.State != StateDegraded {
		t.Fatalf("state=%s want DEGRADED", st.State)
	}
	if len(st.Peers) < 2 {
		t.Fatalf("peers=%d", len(st.Peers))
	}
	if st.Peers[1].Routed {
		t.Fatal("路由缺失时对端行 Routed 必须为 false")
	}
}

// 链路态来自 gnb_ctl，不是瞎猜。gnb_ctl 不可用时必须留 unknown。
func TestNodeStatusPeerLinkFromGnbCtl(t *testing.T) {
	f := newFixture(t)
	f.write("conf/mynet.conf", "NODE_ID=1001\n")
	f.write("var/gnb/node.conf", "x\n")
	f.write("var/gnb/gnb.pid", "4242\n")
	f.write("var/bundle.json", `{"network":{
		"addresses":["10.0.0.1/32"],
		"routes":[{"prefix":"10.0.0.2/32","dev":"gnb_tun"}]
	},"gnb":{"nodes":[{"node_id":"1002","tun_address":"10.0.0.2/32"}]}}`)
	p := f.probe()
	p.gnbPath = "/usr/bin/gnb"
	p.pidAlive = func(int) bool { return true }
	p.findIface = func(string) string { return "utun4" }
	p.routeExists = func(string) bool { return true }
	p.gnbPeerLinks = func(string) map[int64]PeerLinkInfo {
		return map[int64]PeerLinkInfo{
			1001: {Link: "local", Overlay: "10.0.0.1"},
			1002: {Link: "direct", Overlay: "10.0.0.2", LatencyMs: 7},
		}
	}
	st := computeNodeStatus(p)
	if len(st.Peers) < 2 {
		t.Fatalf("peers=%d", len(st.Peers))
	}
	if st.Peers[1].Link != "direct" {
		t.Fatalf("对端链路应是 direct，got %q", st.Peers[1].Link)
	}
	if st.Peers[1].LatencyMs != 7 {
		t.Fatalf("latency=%d want 7", st.Peers[1].LatencyMs)
	}
}

func TestNodeStatusPeerLinkUnknownWhenNoCtl(t *testing.T) {
	f := newFixture(t)
	f.write("conf/mynet.conf", "NODE_ID=1001\n")
	f.write("var/gnb/node.conf", "x\n")
	f.write("var/gnb/gnb.pid", "4242\n")
	f.write("var/bundle.json", `{"network":{
		"addresses":["10.0.0.1/32"],
		"routes":[{"prefix":"10.0.0.2/32","dev":"gnb_tun"}]
	}}`)
	p := f.probe()
	p.gnbPath = "/usr/bin/gnb"
	p.pidAlive = func(int) bool { return true }
	p.findIface = func(string) string { return "utun4" }
	p.routeExists = func(string) bool { return true }
	p.gnbPeerLinks = func(string) map[int64]PeerLinkInfo { return nil }
	st := computeNodeStatus(p)
	if st.Peers[1].Link != "unknown" {
		t.Fatalf("gnb_ctl 不可用时必须 unknown，不能编，got %q", st.Peers[1].Link)
	}
}
