package mynet

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/mynet-club/mynet_agent-V2/shared"
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
