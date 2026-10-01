package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func snapNft() *shared.SystemSnapshot {
	return &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true, "iptables": true, "ip": true}}
}

func snapIpt() *shared.SystemSnapshot {
	return &shared.SystemSnapshot{Capabilities: map[string]bool{"iptables": true, "ip": true}}
}

func mustPolicy(t *testing.T, region, policy string) *domain.ProxyPolicy {
	t.Helper()
	src, mode, err := domain.ResolveProxySplit(region, policy)
	if err != nil {
		t.Fatal(err)
	}
	p := &domain.ProxyPolicy{
		Enabled: true,
		Role:    "client",
		Region:  region,
		Policy:  policy,
		Split: &domain.ProxySplit{
			ListSource: src,
			MatchMode:  mode,
			IPList: &domain.ProxyIPListRef{
				Source: src,
				URL:    "https://cdn.example/" + src + ".txt",
			},
		},
		Exits:           []domain.ProxyExit{{NodeID: "9001", Enabled: true}},
		ForeignDNSRoute: true,
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}

func joinCmds(cmds []routeCmd) string {
	var b strings.Builder
	for _, c := range cmds {
		b.WriteString(strings.Join(c.argv, " "))
		b.WriteString("\n")
	}
	return b.String()
}

func TestProxyClientCmdsHasPolicyRouteAndMark(t *testing.T) {
	p := mustPolicy(t, "domestic", "whitelist")
	rt := ProxyRuntime{
		TunDevice:  "gnb_tun",
		ExitVia:    "10.20.0.250",
		WANDevice:  "eth0",
		WANGateway: "192.0.2.1",
		IPSetFile:  "/var/lib/mynet/interip.txt",
	}
	cmds := proxyClientCmds(snapNft(), p, rt)
	text := joinCmds(cmds)
	for _, want := range []string{
		"nft add table ip mynet_proxy",
		"ip route replace default via 10.20.0.250 dev gnb_tun table 200", // GUARD-OK 测试断言命令串，非执行点
		"ip rule add fwmark 0xc8 lookup 200 prio 31800",
		"ip route replace 192.0.2.1/32 dev eth0", // GUARD-OK 测试断言命令串，非执行点
		"nft add rule ip mangle PREROUTING ip daddr @mynet_proxy dst4 meta mark set 0xc8",
		"ip route replace 8.8.8.8/32 dev gnb_tun", // GUARD-OK 测试断言命令串，非执行点
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少命令 %q\n---\n%s", want, text)
		}
	}
}

func TestProxyMatchModeInvertedUsesNegatedMatch(t *testing.T) {
	// domestic+blacklist → china/inverted：未命中集合 → mark
	p := mustPolicy(t, "domestic", "blacklist")
	if p.Split.MatchMode != domain.ProxyMatchInverted {
		t.Fatalf("match=%s", p.Split.MatchMode)
	}
	rt := ProxyRuntime{TunDevice: "gnb_tun", ExitVia: "10.20.0.250", IPSetFile: "/tmp/chinaip.txt"}
	text := joinCmds(proxyClientCmds(snapNft(), p, rt))
	want := "nft add rule ip mangle PREROUTING ip daddr != @mynet_proxy dst4 meta mark set 0xc8"
	if !strings.Contains(text, want) {
		t.Fatalf("inverted 应使用 != 匹配\n---\n%s", text)
	}
	// normal 矩阵不得出现 !=
	p2 := mustPolicy(t, "domestic", "whitelist")
	text2 := joinCmds(proxyClientCmds(snapNft(), p2, rt))
	if strings.Contains(text2, "daddr !=") {
		t.Fatalf("normal 不该取反:\n%s", text2)
	}
}

func TestProxyIPTablesFallbackUsesIPSet(t *testing.T) {
	p := mustPolicy(t, "international", "whitelist")
	rt := ProxyRuntime{TunDevice: "wg0", ExitVia: "10.0.0.1", IPSetFile: "/tmp/chinaip.txt"}
	text := joinCmds(proxyClientCmds(snapIpt(), p, rt))
	for _, want := range []string{
		"ipset create mynet_proxy hash:net -exist",
		"iptables -t mangle -A PREROUTING -m set --match-set mynet_proxy dst -j MARK --set-mark 0xc8",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("iptables 路径缺 %q\n---\n%s", want, text)
		}
	}
}

func TestProxyServerCmds(t *testing.T) {
	src, mode, _ := domain.ResolveProxySplit("international", "whitelist")
	p := &domain.ProxyPolicy{
		Enabled: true,
		Role:    "server",
		Region:  "international",
		Policy:  "whitelist",
		Split:   &domain.ProxySplit{ListSource: src, MatchMode: mode},
	}
	rt := ProxyRuntime{TunDevice: "gnb_tun"}
	text := joinCmds(proxyServerCmds(snapNft(), p, rt))
	for _, want := range []string{
		"sysctl -w net.ipv4.ip_forward=1",
		"nft add rule ip nat POSTROUTING iifname gnb_tun masquerade",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("server 缺 %q\n%s", want, text)
		}
	}
	// client-only 不该出 NAT
	c := mustPolicy(t, "domestic", "whitelist")
	if got := joinCmds(proxyServerCmds(snapNft(), c, rt)); got != "" {
		t.Fatalf("client 不该生成 server 命令:\n%s", got)
	}
}

func TestProxyCleanupRemovesForeignDNSAndRule(t *testing.T) {
	p := mustPolicy(t, "domestic", "whitelist")
	rt := ProxyRuntime{TunDevice: "gnb_tun"}
	text := joinCmds(proxyCleanupCmds(p, rt))
	for _, want := range []string{
		"ip route del 8.8.8.8/32 dev gnb_tun", // GUARD-OK 测试断言命令串，非执行点
		"ip rule del fwmark 0xc8 lookup 200",
		"ip route flush table 200",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("cleanup 缺 %q\n%s", want, text)
		}
	}
	for _, c := range proxyCleanupCmds(p, rt) {
		if !c.optional {
			t.Fatalf("cleanup 命令应全部 optional: %v", c.argv)
		}
	}
}

func TestProxyDNSRedirect(t *testing.T) {
	p := mustPolicy(t, "domestic", "whitelist")
	p.DNS = &domain.ProxyDNS{Mode: "redirect", RedirectTarget: "10.20.0.250"}
	text := joinCmds(proxyDNSCmds(p, ProxyRuntime{}))
	if !strings.Contains(text, "dnat to 10.20.0.250:53") {
		t.Fatalf("redirect 缺 DNAT:\n%s", text)
	}
}

func TestPlanFromBundleIncludesProxy(t *testing.T) {
	env := &domain.BundleEnvelope{
		SchemaVersion: 2,
		Node:          domain.NodeRef{ID: "1001", Interface: "gnb_tun"},
		Driver:        domain.DriverTypeGNB,
		Network: domain.NetworkIntent{
			Addresses: []string{"10.20.0.1/24"},
			Proxy:     mustPolicy(t, "domestic", "whitelist"),
		},
		Gnb: &domain.GnbIntent{
			Identity: domain.GnbIdentity{NodeID: "1001"},
			Nodes: []domain.GnbNode{
				{NodeID: "9001", TunAddress: "10.20.0.250/24"},
			},
		},
	}
	plan, perr := planFromBundle(env)
	if perr != nil {
		t.Fatal(perr)
	}
	if plan.Proxy == nil {
		t.Fatal("plan.Proxy 为空")
	}
	if plan.Proxy.Runtime.ExitVia != "10.20.0.250" {
		t.Fatalf("ExitVia=%q，应从 gnb.nodes.tun_address 解析", plan.Proxy.Runtime.ExitVia)
	}
	if plan.Proxy.Runtime.TunDevice != "gnb_tun" {
		t.Fatalf("TunDevice=%q", plan.Proxy.Runtime.TunDevice)
	}
	// 出口 VIP 显式给出时优先
	env.Network.Proxy.Exits[0].Via = "10.20.0.99"
	plan2, perr2 := planFromBundle(env)
	if perr2 != nil {
		t.Fatal(perr2)
	}
	if plan2.Proxy.Runtime.ExitVia != "10.20.0.99" {
		t.Fatalf("显式 via 应优先: %q", plan2.Proxy.Runtime.ExitVia)
	}
}

// TestProxyFromFixtureEndToEnd 用双端 fixture 走通
// DecodeBundleEnvelope → planFromBundle → proxyAllCmds。
// 这是 D2 意义上的「打真数据路径」，不是读 fixture skip。
func TestProxyFromFixtureEndToEnd(t *testing.T) {
	// CI 自包含：仓内 testdata 优先；monorepo 再回退 mynet_ctl SSOT
	var raw []byte
	var err error
	for _, dir := range []string{
		filepath.Join("testdata", "bundle-envelope-v2"),
		filepath.Join("..", "testdata", "bundle-envelope-v2"),
		filepath.Join("..", "..", "mynet_ctl", "docs", "knowledge-base", "spec", "fixtures", "bundle-envelope-v2"),
	} {
		raw, err = os.ReadFile(filepath.Join(dir, "gnb-proxy-split.json"))
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("读 fixture: %v", err)
	}
	env, err := domain.DecodeBundleEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if env.Network.Proxy == nil {
		t.Fatal("fixture 缺 network.proxy")
	}
	plan, perr := planFromBundle(env)
	if perr != nil {
		t.Fatal(perr)
	}
	if plan.Proxy == nil {
		t.Fatal("plan.Proxy 为空")
	}
	snap := snapNft()
	cmds := proxyAllCmds(snap, plan.Proxy.Policy, plan.Proxy.Runtime)
	text := joinCmds(cmds)
	// fixture: domestic+whitelist → international/normal + split DNS + foreign dns
	for _, want := range []string{
		"table 200",
		"fwmark 0xc8",
		"daddr @mynet_proxy dst4 meta mark set 0xc8",
		"ip route replace 8.8.8.8/32", // GUARD-OK 测试断言命令串，非执行点
		// mynetBinPath() 给绝对路径（服务环境 PATH 不含 mynet）——只查子命令
		"proxy dns-split 223.5.5.5,119.29.29.29 8.8.8.8",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("fixture 链缺 %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "daddr !=") {
		t.Fatalf("whitelist/normal 不该取反:\n%s", text)
	}
}

func TestProxyMarkCoversOUTPUTAndPREROUTING(t *testing.T) {
	p := mustPolicy(t, "domestic", "whitelist")
	rt := ProxyRuntime{TunDevice: "gnb_tun", ExitVia: "10.20.0.250", IPSetFile: "/tmp/x"}
	text := joinCmds(proxyMarkCmds(snapNft(), p, rt, 0xc8))
	if !strings.Contains(text, "chain ip mangle OUTPUT") {
		t.Fatalf("同机必须打 OUTPUT:\n%s", text)
	}
	if !strings.Contains(text, "chain ip mangle PREROUTING") {
		t.Fatalf("LAN 仍要 PREROUTING:\n%s", text)
	}
	if !strings.Contains(text, "OUTPUT ip daddr @mynet_proxy dst4 meta mark set 0xc8") {
		t.Fatalf("OUTPUT mark 缺:\n%s", text)
	}
}

func TestProxySelfExemptBeforeMark(t *testing.T) {
	p := mustPolicy(t, "domestic", "whitelist")
	rt := ProxyRuntime{
		TunDevice:       "gnb_tun",
		ExitVia:         "10.20.0.250",
		ExitEndpoint:    "203.0.113.10",
		TunnelLocalPort: 9016,
		WANDevice:       "eth0",
		WANGateway:      "192.0.2.1",
		IPSetFile:       "/tmp/x",
	}
	cmds := proxyClientCmds(snapNft(), p, rt)
	text := joinCmds(cmds)
	// 豁免
	for _, want := range []string{
		"ip route replace 203.0.113.10/32 via 192.0.2.1 dev eth0", // GUARD-OK 测试断言命令串，非执行点
		"nft add rule ip mangle OUTPUT ip daddr 203.0.113.10 return",
		"nft add rule ip mangle OUTPUT udp sport 9016 return",
		"nft add rule ip mangle OUTPUT oifname lo return",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺自环豁免 %q\n%s", want, text)
		}
	}
	// 顺序：豁免 return 必须在 OUTPUT mark 之前
	acc, mark := -1, -1
	for i, c := range cmds {
		s := strings.Join(c.argv, " ")
		if acc < 0 && strings.Contains(s, "daddr 203.0.113.10 return") {
			acc = i
		}
		if mark < 0 && strings.Contains(s, "mangle OUTPUT") && strings.Contains(s, "meta mark set") {
			mark = i
		}
	}
	if acc < 0 || mark < 0 || acc > mark {
		t.Fatalf("endpoint 豁免须在 mark 之前: exempt=%d mark=%d", acc, mark)
	}
}
