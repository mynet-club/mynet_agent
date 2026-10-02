package mynet

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/mynet-club/mynet_agent/shared"
)

func snapIP() *shared.SystemSnapshot {
	return &shared.SystemSnapshot{Capabilities: map[string]bool{"ip": true}}
}

func snapNoIP() *shared.SystemSnapshot {
	return &shared.SystemSnapshot{OS: "darwin", Capabilities: map[string]bool{}}
}

func TestApplyPlanRouteArgv(t *testing.T) {
	r := RouteOp{Destination: "10.8.0.0/24", Device: "gnb0", Action: "replace"}
	cmds := routeCmds(snapIP(), r)
	if len(cmds) != 1 {
		t.Fatalf("cmds=%v", cmds)
	}
	want := []string{"ip", "route", "replace", "10.8.0.0/24", "dev", "gnb0"}
	argv := cmds[0].argv
	if len(argv) != len(want) {
		t.Fatalf("argv=%v want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv=%v want %v", argv, want)
		}
	}
}

// darwin（无 ip 命令）必须落到 route 方言：主机路由去 /32，replace = 先删后加。
func TestRouteCmdsBSDHostReplace(t *testing.T) {
	r := RouteOp{Destination: "10.0.0.16/32", Device: "utun4", Action: "replace"}
	cmds := routeCmds(snapNoIP(), r)
	if len(cmds) != 2 {
		t.Fatalf("replace 应是 delete+add 两步，got %v", cmds)
	}
	if !cmds[0].optional {
		t.Fatal("前置 delete 必须 optional（路由不存在时的失败不是错误）")
	}
	wantDel := []string{"route", "-n", "delete", "-host", "10.0.0.16"}
	wantAdd := []string{"route", "-n", "add", "-host", "10.0.0.16", "-interface", "utun4"}
	if !argvEq(cmds[0].argv, wantDel) {
		t.Fatalf("del=%v want %v", cmds[0].argv, wantDel)
	}
	if !argvEq(cmds[1].argv, wantAdd) {
		t.Fatalf("add=%v want %v", cmds[1].argv, wantAdd)
	}
}

func TestRouteCmdsBSDNetWithGateway(t *testing.T) {
	r := RouteOp{Destination: "10.8.0.0/24", Gateway: "10.0.0.1", Action: "add"}
	cmds := routeCmds(snapNoIP(), r)
	want := []string{"route", "-n", "add", "-net", "10.8.0.0/24", "10.0.0.1"}
	if len(cmds) != 1 || !argvEq(cmds[0].argv, want) {
		t.Fatalf("got %v want %v", cmds, want)
	}
}

func TestRouteCmdsBSDIgnoresMetric(t *testing.T) {
	r := RouteOp{Destination: "10.0.0.16/32", Device: "utun4", Metric: 100, Action: "add"}
	cmds := routeCmds(snapNoIP(), r)
	if len(cmds) != 1 {
		t.Fatalf("cmds=%v", cmds)
	}
	for _, a := range cmds[0].argv {
		if a == "metric" || a == "100" {
			t.Fatalf("bsd route 不该拼 metric: %v", cmds[0].argv)
		}
	}
}

// 逻辑名 gnb_tun 在系统里不存在时，必须用本机 overlay IP 反查真实接口。
// 这是 darwin/gnb 的真实形态（gnb_drv_darwin.c 把 ifname 覆盖成 utunN）。
func TestResolveDeviceFallsBackToOverlayIP(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil || len(ifaces) == 0 {
		t.Skip("读不到本机接口")
	}
	// 挑一个带 IPv4 的接口，拿它的真 IP 当 overlay 地址
	var real, ipCIDR string
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			real = iface.Name
			ipCIDR = ipnet.String()
			break
		}
		if real != "" {
			break
		}
	}
	if real == "" {
		t.Skip("没有带 IPv4 的接口")
	}

	got := resolveDevice("gnb_tun_zzz_no_such_if", []string{ipCIDR})
	if got != real {
		t.Fatalf("resolveDevice(不存在的名字, %s)=%q want %q", ipCIDR, got, real)
	}
	// 真实名字必须原样保留
	if got := resolveDevice(real, []string{"203.0.113.1/32"}); got != real {
		t.Fatalf("resolveDevice(%s)=%s want %s", real, got, real)
	}
}

func TestResolveDeviceKeepsNameWhenUnknown(t *testing.T) {
	if got := resolveDevice("gnb_tun", []string{"203.0.113.1/32"}); got != "gnb_tun" {
		t.Fatalf("解析不出来时必须原样返回（宁可失败也不装错网卡），got %q", got)
	}
}

func TestIsHostDest(t *testing.T) {
	cases := map[string]bool{
		"10.0.0.16/32": true,
		"10.0.0.16":    true,
		"10.8.0.0/24":  false,
		"0.0.0.0/0":    false,
	}
	for dest, want := range cases {
		if got := isHostDest(dest); got != want {
			t.Fatalf("isHostDest(%s)=%v want %v", dest, got, want)
		}
	}
}

func TestIfaceArgvBSD(t *testing.T) {
	argv := ifaceArgv(snapNoIP(), IfaceOp{Name: "utun4", Action: "up"})
	want := []string{"ifconfig", "utun4", "up"}
	if !argvEq(argv, want) {
		t.Fatalf("got %v want %v", argv, want)
	}
	argv = ifaceArgv(snapIP(), IfaceOp{Name: "gnb_tun", Action: "down"})
	want = []string{"ip", "link", "set", "dev", "gnb_tun", "down"}
	if !argvEq(argv, want) {
		t.Fatalf("got %v want %v", argv, want)
	}
}

func TestFirewallMasqPicksToolFromSnapshot(t *testing.T) {
	snapNft := &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true, "iptables": true}}
	argv := firewallArgv(snapNft, FirewallOp{Action: "ensure-masq", Device: "wg0"})
	if argv[0] != "nft" {
		t.Fatalf("prefer nft, got %v", argv)
	}
	snapIpt := &shared.SystemSnapshot{Capabilities: map[string]bool{"iptables": true}}
	argv = firewallArgv(snapIpt, FirewallOp{Action: "ensure-masq", Device: "gnb0"})
	if argv[0] != "iptables" {
		t.Fatalf("prefer iptables, got %v", argv)
	}
	// 跨驱动：wg0 与 gnb0 对 apply 无差别
	if len(firewallArgv(snapIpt, FirewallOp{Action: "ensure-masq", Device: "wg0"})) == 0 {
		t.Fatal("wg0 must be treated same as gnb0")
	}
}

func TestExecuteApplyDryRun(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	plan := `{"routes":[{"destination":"10.9.0.0/24","device":"wg0","action":"replace"}]}`
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := readApplyPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Routes) != 1 {
		t.Fatalf("routes=%v", p.Routes)
	}
}

func TestRunHelp(t *testing.T) {
	if code := Run([]string{"help"}); code != 0 {
		t.Fatalf("help exit=%d", code)
	}
	if code := Run(nil); code != 2 {
		t.Fatalf("noargs exit=%d", code)
	}
}

func argvEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// daemon 的 PATH 往往没有 /sbin，而 route/ifconfig 就在那儿。
func TestResolveBinFindsSystemSbin(t *testing.T) {
	// 清掉 PATH 里常见的 sbin，模拟「只给了 /usr/bin:/bin」的 daemon
	old := os.Getenv("PATH")
	os.Setenv("PATH", "/usr/bin:/bin")
	defer os.Setenv("PATH", old)

	p := resolveBin("route")
	if p == "route" {
		// 本机可能真的没有 route（如精简容器），跳过而不是误报
		if _, err := os.Stat("/sbin/route"); err != nil {
			t.Skip("本机没有 /sbin/route")
		}
		t.Fatal("resolveBin(route) 应扫到 /sbin/route，而不是原样返回")
	}
	if !filepath.IsAbs(p) {
		t.Fatalf("resolveBin(route)=%q 应是绝对路径", p)
	}
}

// 对端 LAN 与本机直连网段同网段时，装隧道路由会劫持局域网
// （delete net 192.168.0.0 拆掉 en0 直连）。必须在 executeApply 前滤掉。
func TestOverlapsAnyDetectsHijack(t *testing.T) {
	local := []*net.IPNet{mustCIDR(t, "192.168.0.0/24")}
	cases := []struct {
		dest string
		want bool
	}{
		{"192.168.0.0/24", true},  // 同网段
		{"192.168.0.0/16", true},  // 更宽，盖住本机
		{"192.168.0.1/32", true},  // 本机网段里的主机
		{"192.168.8.0/24", false}, // 对端 LAN，不冲突
		{"10.182.236.182/32", false},
		{"192.168.100.0/24", false},
	}
	for _, c := range cases {
		got := overlapsAny(parseDestNet(c.dest), local)
		if got != c.want {
			t.Fatalf("overlapsAny(%s)=%v want %v", c.dest, got, c.want)
		}
	}
}

func TestParseDestNet(t *testing.T) {
	n := parseDestNet("192.168.8.0/24")
	if n == nil || n.String() != "192.168.8.0/24" {
		t.Fatalf("got %v", n)
	}
	h := parseDestNet("10.0.0.16/32")
	if h == nil || h.String() != "10.0.0.16/32" {
		t.Fatalf("got %v", h)
	}
	if parseDestNet("fd00::1/128") != nil {
		t.Fatal("IPv6 dest 应返回 nil（本函数只做 IPv4 冲突判断）")
	}
}

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// 「只删自己配置的」：目的地空闲或挂在自己的隧道口才准 replace/del；
// 挂在直连口/别人的路由上一律不碰。
func TestRouteTouchedOnlyOwn(t *testing.T) {
	cases := []struct {
		owner, ourDev string
		want          bool
	}{
		{"", "utun7", true},       // 空闲 → 可装
		{"utun7", "utun7", true},  // 自己装的 → 可改/可删
		{"en0", "utun7", false},   // 系统直连 → 不碰
		{"utun8", "utun7", false}, // 别的隧道 → 不碰
		{"eth0", "gnb0", false},   // 别人的静态路由 → 不碰
		{"utun7", "", false},      // 没有目标设备时无法主张所有权
	}
	for _, c := range cases {
		if got := routeTouched(c.owner, c.ourDev); got != c.want {
			t.Fatalf("routeTouched(%q,%q)=%v want %v", c.owner, c.ourDev, got, c.want)
		}
	}
}

func snapWin() *shared.SystemSnapshot {
	return &shared.SystemSnapshot{OS: "windows", Capabilities: map[string]bool{}}
}

func TestNetshRouteCmds(t *testing.T) {
	// 语法钉 MicrosoftDocs netsh-interface.md：具名 prefix=/interface=/store=active
	r := RouteOp{Destination: "192.168.8.0/24", Device: "P2PNet", Action: "replace"}
	cmds := routeCmds(snapWin(), r)
	if len(cmds) != 2 {
		t.Fatalf("replace 应是 delete+add，got %v", cmds)
	}
	wantDel := []string{"netsh", "interface", "ipv4", "delete", "route", "prefix=192.168.8.0/24", "interface=P2PNet", "store=active"}
	wantAdd := []string{"netsh", "interface", "ipv4", "add", "route", "prefix=192.168.8.0/24", "interface=P2PNet", "store=active"}
	if !argvEq(cmds[0].argv, wantDel) {
		t.Fatalf("del=%v want %v", cmds[0].argv, wantDel)
	}
	if !argvEq(cmds[1].argv, wantAdd) {
		t.Fatalf("add=%v want %v", cmds[1].argv, wantAdd)
	}

	h := RouteOp{Destination: "10.0.0.16/32", Device: "P2PNet", Gateway: "10.0.0.16", Action: "add"}
	hc := routeCmds(snapWin(), h)
	wantHost := []string{"netsh", "interface", "ipv4", "add", "route", "prefix=10.0.0.16/32", "interface=P2PNet", "nexthop=10.0.0.16", "store=active"}
	if len(hc) != 1 || !argvEq(hc[0].argv, wantHost) {
		t.Fatalf("host=%v want %v", hc, wantHost)
	}
}

func TestNormalizeBSDDest(t *testing.T) {
	cases := map[string]string{
		"192.168.8":      "192.168.8.0/24",
		"10.8":           "10.8.0.0/16",
		"127":            "127.0.0.0/8",
		"10.0.0.16":      "10.0.0.16/32",
		"192.168.8.0/24": "192.168.8.0/24",
	}
	for in, want := range cases {
		if got := normalizeBSDDest(in); got != want {
			t.Fatalf("normalizeBSDDest(%q)=%q want %q", in, got, want)
		}
	}
}
