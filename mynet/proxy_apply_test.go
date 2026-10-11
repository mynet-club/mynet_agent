package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// mustPermission 云端下发的出口许可（D1 收窄后 wire 的 network.proxy）。
func mustPermission() *domain.ProxyPermission {
	return &domain.ProxyPermission{Enabled: true}
}

// seedLocalProxy 把客户端策略落到 intent Local 轨（D1 收窄后客户端策略的唯一来源）。
func seedLocalProxy(t *testing.T, p *domain.ProxyPolicy) {
	t.Helper()
	st, err := LoadProxyState()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyLocal(*p, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveProxyState(st); err != nil {
		t.Fatal(err)
	}
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
		"nft add rule ip mynet_proxy PREROUTING ip daddr @mynet_proxy meta mark set 0xc8",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少命令 %q\n---\n%s", want, text)
		}
	}
	// 国外 DNS /32 主表钉扎已下线（2026-10-11）：写主表会让**节点自身**的解析流量
	// 也进隧道（节点自己代理自己）。客户端靠上面那条打标即可。
	if strings.Contains(text, "route replace 8.8.8.8/32") {
		t.Fatalf("国外 DNS /32 不得再钉进主表:\n%s", text)
	}
}

func TestProxyBSDNeverTouchesMainDefault(t *testing.T) {
	// 事故回归（#21）：macOS route 删 default 不带网关会删掉**全部** IPv4 默认路由
	// （含物理网关），历史代码在 BSD 分支发 replace default → 整机断网。
	// 契约：BSD/macOS 下 proxy 命令计划绝不允许出现 `-net default` / 主表 default。
	p := mustPolicy(t, "domestic", "whitelist")
	p.FullTunnel = true
	p.DoHMode = DoHModeTunnel
	rt := ProxyRuntime{TunDevice: "gnb_tun", ExitVia: "10.20.0.250", IPSetFile: "/tmp/x"}
	snap := &shared.SystemSnapshot{OS: "darwin", Capabilities: map[string]bool{}}
	text := joinCmds(proxyAllCmds(snap, p, rt))
	if strings.Contains(text, "-net default") || strings.Contains(text, " default ") {
		t.Fatalf("BSD 计划不得触碰 default:\n%s", text)
	}
	// 国外 DNS /32 主表钉扎已下线（2026-10-11，跨平台一致）：BSD 也不得再出现该 host 路由。
	if strings.Contains(text, "add -host 8.8.8.8") {
		t.Fatalf("BSD 也不得钉国外 DNS /32 进主表:\n%s", text)
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
	want := "nft add rule ip mynet_proxy PREROUTING ip daddr != @mynet_proxy meta mark set 0xc8"
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
	text := joinCmds(proxyCleanupCmds(snapNft(), p, rt))
	for _, want := range []string{
		"ip route del 8.8.8.8/32 dev gnb_tun", // GUARD-OK 测试断言命令串，非执行点
		"ip rule del fwmark 0xc8 lookup 200",
		"ip route flush table 200",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("cleanup 缺 %q\n%s", want, text)
		}
	}
	for _, c := range proxyCleanupCmds(snapNft(), p, rt) {
		if !c.optional {
			t.Fatalf("cleanup 命令应全部 optional: %v", c.argv)
		}
	}
	// BSD 方言：只撤 /32 钉扎（route del 不触碰 default），且不得出现任何 default 命令
	snapBSD := &shared.SystemSnapshot{OS: "darwin", Capabilities: map[string]bool{}}
	bsdText := joinCmds(proxyCleanupCmds(snapBSD, p, rt))
	if !strings.Contains(bsdText, "route -n delete -host 8.8.8.8") { // GUARD-OK 测试断言命令串，非执行点
		t.Fatalf("BSD cleanup 应撤国外 DNS 钉扎:\n%s", bsdText)
	}
	if strings.Contains(bsdText, "default") || strings.Contains(bsdText, "ip route") || strings.Contains(bsdText, "ip rule") {
		t.Fatalf("BSD cleanup 不该出现策略路由/default:\n%s", bsdText)
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
	t.Setenv("MYNET_HOME", t.TempDir()) // intent 落轨写的是本机状态，测试必须隔离
	// 客户端策略只在 Local 轨（wire 只带许可），出口解析走 Local 的 exits
	seedLocalProxy(t, mustPolicy(t, "domestic", "whitelist"))
	env := &domain.BundleEnvelope{
		SchemaVersion: 2,
		Node:          domain.NodeRef{ID: "1001", Interface: "gnb_tun"},
		Driver:        domain.DriverTypeGNB,
		Network: domain.NetworkIntent{
			Addresses: []string{"10.20.0.1/24"},
			Proxy:     mustPermission(),
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
	// 出口 VIP 显式给出时优先（改 Local 轨出口的 via）
	lp := mustPolicy(t, "domestic", "whitelist")
	lp.Exits[0].Via = "10.20.0.99"
	seedLocalProxy(t, lp)
	plan2, perr2 := planFromBundle(env)
	if perr2 != nil {
		t.Fatal(perr2)
	}
	if plan2.Proxy.Runtime.ExitVia != "10.20.0.99" {
		t.Fatalf("显式 via 应优先: %q", plan2.Proxy.Runtime.ExitVia)
	}
}

// TestPlanFromBundleComposesLocalClientAndCloudPermission —— D1 收窄后两轨分工：
// client 侧策略取 Local 轨，server 侧许可（ACL）取 Cloud 轨。
// 历史回归（#21-B「本地关闭被云端复活」）在新模型下结构性消失：wire 本就不再下发
// client 策略，云端无从复活本地开关；云端许可只驱动 server 侧。
func TestPlanFromBundleComposesLocalClientAndCloudPermission(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	env := &domain.BundleEnvelope{
		SchemaVersion: 2,
		Node:          domain.NodeRef{ID: "1001", Interface: "gnb_tun"},
		Driver:        domain.DriverTypeGNB,
		Network: domain.NetworkIntent{
			Addresses: []string{"10.20.0.1/24"},
			Proxy: &domain.ProxyPermission{
				Enabled: true,
				ACL:     &domain.ProxyACL{Mode: domain.ProxyACLAllowlist, AllowedNodeIDs: []string{"9001"}},
			},
		},
		Gnb: &domain.GnbIntent{Identity: domain.GnbIdentity{NodeID: "1001"}},
	}
	// ① 只给云端许可（无 Local）：纯 server 侧计划，带 ACL
	plan, err := planFromBundle(env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Proxy == nil {
		t.Fatal("云端许可应生成 proxy 计划（server 侧）")
	}
	if plan.Proxy.Policy.Role != domain.ProxyRoleServer {
		t.Fatalf("只带许可时 role=%q，应为 server", plan.Proxy.Policy.Role)
	}
	if !plan.Proxy.Policy.IsServerLike() || plan.Proxy.Policy.IsClientLike() {
		t.Fatalf("只带许可时应 server-like 且非 client-like: %+v", plan.Proxy.Policy)
	}
	if plan.Proxy.Policy.ACL == nil {
		t.Fatal("ACL 应取自云端许可")
	}

	// ② 本地开 client：合成 both，client 侧策略来自 Local
	seedLocalProxy(t, mustPolicy(t, "domestic", "whitelist"))
	plan2, err := planFromBundle(env)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Proxy == nil || plan2.Proxy.Policy.Role != domain.ProxyRoleBoth {
		t.Fatalf("Local client + 云端许可 → role 应为 both: %+v", plan2.Proxy)
	}
	if plan2.Proxy.Policy.Split == nil || plan2.Proxy.Policy.Split.ListSource != domain.ProxyListInternational {
		t.Fatalf("client 侧 split 应来自 Local: %+v", plan2.Proxy.Policy.Split)
	}

	// ③ 本地关 client：合成退回纯 server，云端许可不受影响
	st, err := LoadProxyState()
	if err != nil {
		t.Fatal(err)
	}
	off := mustPolicy(t, "domestic", "whitelist")
	off.Enabled = false
	if err := st.ApplyLocal(*off, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveProxyState(st); err != nil {
		t.Fatal(err)
	}
	plan3, err := planFromBundle(env)
	if err != nil {
		t.Fatal(err)
	}
	if plan3.Proxy == nil || plan3.Proxy.Policy.Role != domain.ProxyRoleServer || plan3.Proxy.Policy.IsClientLike() {
		t.Fatalf("本地关 client 后应为纯 server: %+v", plan3.Proxy)
	}
}

// TestProxyFromFixtureEndToEnd 用双端 fixture 走通
// DecodeBundleEnvelope → planFromBundle → proxyAllCmds。
// 这是 D2 意义上的「打真数据路径」，不是读 fixture skip。
func TestProxyFromFixtureEndToEnd(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
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
	// D1 收窄后 wire 只带许可；client 侧分流策略归 Local 轨。fixture 的 client 意图
	// （domestic+whitelist → international/normal + split DNS + foreign dns）落 Local。
	seedLocalProxy(t, &domain.ProxyPolicy{
		Enabled: true,
		Role:    domain.ProxyRoleClient,
		Region:  domain.ProxyRegionDomestic,
		Policy:  domain.ProxyPolicyWhitelist,
		Split: &domain.ProxySplit{
			ListSource: domain.ProxyListInternational,
			MatchMode:  domain.ProxyMatchNormal,
			IPList: &domain.ProxyIPListRef{
				Source: domain.ProxyListInternational,
				URL:    "https://cdn.mynet.club/proxy-lists/interip.txt",
			},
		},
		Exits: []domain.ProxyExit{{NodeID: "9001", Weight: 20, Order: 1, Enabled: true}},
		DNS: &domain.ProxyDNS{
			Mode:            "split",
			Server:          "8.8.8.8",
			DomesticServers: []string{"223.5.5.5", "119.29.29.29"},
		},
		ForeignDNSRoute: true,
		Fwmark:          200,
		Table:           200,
	})
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
	// fixture: domestic+whitelist → international/normal + split DNS（外部 DNS 主表钉扎已下线）
	for _, want := range []string{
		"table 200",
		"fwmark 0xc8",
		"daddr @mynet_proxy meta mark set 0xc8",
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
	if !strings.Contains(text, "chain ip mynet_proxy OUTPUT") {
		t.Fatalf("同机必须打 OUTPUT:\n%s", text)
	}
	if !strings.Contains(text, "chain ip mynet_proxy PREROUTING") {
		t.Fatalf("LAN 仍要 PREROUTING:\n%s", text)
	}
	if !strings.Contains(text, "OUTPUT ip daddr @mynet_proxy meta mark set 0xc8") {
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
		"nft add rule ip mynet_proxy OUTPUT ip daddr 203.0.113.10 return",
		"nft add rule ip mynet_proxy OUTPUT udp sport 9016 return",
		"nft add rule ip mynet_proxy OUTPUT oifname lo return",
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
		if mark < 0 && strings.Contains(s, "mynet_proxy OUTPUT") && strings.Contains(s, "meta mark set") {
			mark = i
		}
	}
	if acc < 0 || mark < 0 || acc > mark {
		t.Fatalf("endpoint 豁免须在 mark 之前: exempt=%d mark=%d", acc, mark)
	}
}

// 撤销信封省略许可时，旧 Cloud 轨必须失效，Local client 保持原样。
func TestProxyPermissionRevocationClearsCloud(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	env := &domain.BundleEnvelope{SchemaVersion: 2, Generation: "1", Node: domain.NodeRef{ID: "1001", Interface: "gnb_tun"}, Driver: domain.DriverTypeGNB, Network: domain.NetworkIntent{Proxy: mustPermission()}, Gnb: &domain.GnbIntent{Identity: domain.GnbIdentity{NodeID: "1001"}}}
	plan, err := planFromBundle(env)
	if err != nil || plan.Proxy == nil {
		t.Fatalf("授权应生效: %v", err)
	}
	env.Generation = "2"
	env.Network.Proxy = nil
	plan, err = planFromBundle(env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Proxy != nil {
		t.Fatal("撤销后旧 Cloud 许可仍生效")
	}
	if plan.ProxyExitDevice != "gnb_tun" || !plan.LegacyProxyExit {
		t.Fatal("撤销且无路由时丢失出口清理目标")
	}
	seedLocalProxy(t, mustPolicy(t, "domestic", "whitelist"))
	plan, err = planFromBundle(env)
	if err != nil || plan.Proxy == nil || plan.Proxy.Policy.Role != domain.ProxyRoleClient {
		t.Fatalf("撤销不能删除 Local client: %+v %v", plan, err)
	}
}

// API 真实 publisher 的产物跨语言校验并逐代投影到 apply 计划。
func TestProxyPermissionPublisherLoop(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	for _, state := range []string{"allowlist", "open", "revoked"} {
		raw, err := os.ReadFile(filepath.Join("..", "testdata", "proxy-permission-loop", state+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = domain.VerifyBundleChecksum(raw); err != nil {
			t.Fatal(err)
		}
		env, err := domain.DecodeBundleEnvelope(raw)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := planFromBundle(env)
		if err != nil {
			t.Fatal(err)
		}
		if state == "revoked" {
			if plan.Proxy != nil {
				t.Fatal("真实撤销信封不能保留 Cloud 出口许可")
			}
			continue
		}
		if plan.Proxy == nil || plan.Proxy.Policy.Role != domain.ProxyRoleServer {
			t.Fatalf("%s 未生成 server 计划", state)
		}
		if state == "allowlist" {
			acl := plan.Proxy.Policy.ACL
			if acl == nil || len(acl.AllowedCIDRs) != 2 {
				t.Fatalf("真实白名单 CIDR 丢失: %+v", acl)
			}
			for _, cidr := range acl.AllowedCIDRs {
				if !strings.HasSuffix(cidr, "/32") {
					t.Fatalf("白名单扩大网段: %s", cidr)
				}
			}
		}
		corrupt := strings.Replace(string(raw), "\"enabled\":true", "\"enabled\":false", 1)
		if corrupt == string(raw) || domain.VerifyBundleChecksum([]byte(corrupt)) == nil {
			t.Fatal("篡改许可未被 checksum 拒绝")
		}
	}
}
