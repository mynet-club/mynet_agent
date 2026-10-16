package mynet

import (
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

// flattenCmds 把命令序列摊平成 argv 列表（断言用）。
func flattenCmds(cs []routeCmd) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.argv...)
	}
	return out
}

// 上层合同（平台无关）的契约测试：规则集形状、两个渲染目标、持久化面选择、
// dev 注入安全。这些在生产机（darwin 开发机 / Linux / OpenWrt）上都要能跑
// —— 所以合同层刻意不带构建标签。
//
// 凡入口函数（FirewallIncludeScript / FirewallNftRules）都经 firewallWANResolver
// 问系统拿默认出接口；测试一律**钉死解析器**，否则某台有真实 system.json 的开发机
// 会多出网关规则、断言随机红。纯核（FirewallReplayRules / firewall*Script）显式传参。

// withWAN 同时钉死「出接口」与「网关意图」两个入口解析器：
// firewallResolvedWAN() 先看意图再解析出接口，只钉一个会让网关规则静默消失。
// wan=="" 表示非网关节点（有意不出转发规则），wan 非空表示网关 + 该出接口。
func withWAN(t *testing.T, wan string) {
	t.Helper()
	prevWAN := firewallWANResolver
	prevGW := firewallGatewayResolver
	firewallWANResolver = func() string { return wan }
	firewallGatewayResolver = func() bool { return wan != "" }
	t.Cleanup(func() {
		firewallWANResolver = prevWAN
		firewallGatewayResolver = prevGW
	})
}

func TestFirewallReplayRules(t *testing.T) {
	rules, err := FirewallReplayRules("gnb_tun", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("只有 tun 时 = 2 条（oif/in 各一），得到 %d: %+v", len(rules), rules)
	}
	if rules[0].Dir != FirewallDirOut || rules[1].Dir != FirewallDirIn {
		t.Fatalf("方向应为 out/in: %+v", rules)
	}
	for _, r := range rules {
		if r.Kind != FirewallRuleMasq || r.Dev != "gnb_tun" {
			t.Fatalf("规则形状不符: %+v", r)
		}
	}

	// 空 dev = bundle 未就绪 → 空集（产物出骨架），不得报错
	if rules, err := FirewallReplayRules("", ""); err != nil || len(rules) != 0 {
		t.Fatalf("空 dev 应返回空集: %v %+v", err, rules)
	}
	// 注入面：非法/超长接口名一律拒（会被拼进 sh/nft 文本）
	if _, err := FirewallReplayRules("bad;rm -rf /", ""); err == nil {
		t.Fatal("非法入接口名必须拒")
	}
	if _, err := FirewallReplayRules("gnb_tun", "bad;rm -rf /"); err == nil {
		t.Fatal("非法出接口名必须拒")
	}
	if _, err := FirewallReplayRules("waytoolongdevicename", ""); err == nil {
		t.Fatal("超长接口名必须拒")
	}
}

// 网关转发规则：iif 隧道 + oif 出口两向匹配（「对转发做 nat」的精确写法）。
// wan 缺失或与 tun 相同都不出这条 —— 否则只有 iif 会把隧道间流量也 NAT 掉。
func TestFirewallReplayRulesGatewayForward(t *testing.T) {
	rules, err := FirewallReplayRules("gnb_tun", "enp4s3")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatalf("tun+wan 应为 3 条，得到 %d: %+v", len(rules), rules)
	}
	gw := rules[2]
	if gw.Dir != FirewallDirForward || gw.Dev != "gnb_tun" || gw.EgressDev != "enp4s3" {
		t.Fatalf("网关规则形状不符: %+v", gw)
	}
	if gw.Comment != GatewayForwardComment {
		t.Fatalf("网关规则要带标识注释（doctor 靠它辨认）: %+v", gw)
	}

	// 只有 iif 时不得出网关规则
	if rules, _ := FirewallReplayRules("gnb_tun", "gnb_tun"); len(rules) != 2 {
		t.Fatalf("wan==tun 不应出网关规则: %+v", rules)
	}
}

// include 内容契约：持久化语义（reload 重放 NAT）+ 幂等（先查后加）+
// dev 注入安全（非法字符拒）+ **落自有表**（与实装同一张）。
func TestFirewallIncludeScript(t *testing.T) {
	withWAN(t, "") // 钉死：不带网关规则
	body, err := FirewallIncludeScript("gnb_tun")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`DEV="gnb_tun"`,
		"masquerade",
		"grep -q", // 幂等判据
		"exit 0",
		FirewallNatFamily + " " + FirewallNatTable, // 自有表，不是 ip nat
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("include 缺 %q:\n%s", want, body)
		}
	}
	// 反不变量：不得往别人的 `ip nat` 里加规则（那是 iptables-nft 的管辖面，
	// iptables-restore 整表恢复会把我们冲掉）
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "nft add table ip nat") ||
			strings.Contains(line, "nft add rule ip nat ") {
			t.Fatalf("重放产物不得写 ip nat（应写自有表 %s %s）:\n%s",
				FirewallNatFamily, FirewallNatTable, body)
		}
	}
	// 双向两条都在（oif/iif），与 OpenWrt 既有语义逐字一致
	if n := strings.Count(body, "nft add rule"); n != 2 {
		t.Fatalf("应渲染 2 条 masq 规则，得到 %d:\n%s", n, body)
	}
	if !strings.Contains(body, `oifname "$DEV"`) || !strings.Contains(body, `iifname "$DEV"`) {
		t.Fatalf("oif/iif 双向匹配式缺失:\n%s", body)
	}

	if _, err := FirewallIncludeScript("bad;rm -rf /"); err == nil {
		t.Fatal("非法接口名必须拒")
	}
	if _, err := FirewallIncludeScript("waytoolongdevicename"); err == nil {
		t.Fatal("超长接口名必须拒")
	}
	// 空 dev = 骨架（bundle 未就绪），不得报错
	if body, err := FirewallIncludeScript(""); err != nil || !strings.Contains(body, `DEV=""`) {
		t.Fatalf("空 dev 应出骨架: %v\n%s", err, body)
	}
}

// 网关规则也要进重放产物 —— 否则 OpenWrt 侧 fw4 reload 后，实装的网关 NAT 丢了、
// 两条旧规则却还在（重放面与实装面不一致，比全丢更难查）。
func TestFirewallIncludeScriptGatewayRule(t *testing.T) {
	withWAN(t, "enp4s3")
	body, err := FirewallIncludeScript("gnb_tun")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `iifname "$DEV" oifname "$WAN"`) {
		t.Fatalf("缺网关两向匹配:\n%s", body)
	}
	if !strings.Contains(body, GatewayForwardComment) {
		t.Fatalf("缺网关规则注释:\n%s", body)
	}
	if !strings.Contains(body, `WAN="enp4s3"`) {
		t.Fatalf("缺出接口赋值:\n%s", body)
	}
	if n := strings.Count(body, "nft add rule"); n != 3 {
		t.Fatalf("tun+wan 应渲染 3 条，得到 %d:\n%s", n, body)
	}
}

// nft 渲染目标：必须用 mynet 自有表，不能抢 iptables-nft 的 `table ip nat`；
// tun 未就绪时输出为空（执行面据此清掉上一版）。
func TestFirewallNftRules(t *testing.T) {
	withWAN(t, "enp4s3")
	body, err := FirewallNftRules("gnb_tun")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		// 幂等前导（缺了 nft -f 重复加载会堆规则，见本函数注释）
		"add table ip mynet",
		"flush table ip mynet",
		"table ip mynet {",
		"type nat hook postrouting priority srcnat;",
		`oifname "gnb_tun" masquerade`,
		`iifname "gnb_tun" masquerade`,
		`iifname "gnb_tun" oifname "enp4s3" masquerade`,
		GatewayForwardComment,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("nft 规则缺 %q:\n%s", want, body)
		}
	}
	// 反不变量：不得**声明** `table ip nat`（iptables-nft 在用它，会 File exists）。
	// 只查声明行 —— 注释里提到这个名字是正常的说明文字。
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "table ip nat") {
			t.Fatalf("不得与 iptables-nft 抢 `table ip nat`:\n%s", body)
		}
	}
	if body, err := FirewallNftRules(""); err != nil || body != "" {
		t.Fatalf("空 dev 应输出空: %v %q", err, body)
	}
}

// add table 必须排在 flush table 之前 —— 表还不存在时 nft -f 事务性中止，
// 顺序反了首装会直接失败。
func TestFirewallNftRulesAddBeforeFlush(t *testing.T) {
	withWAN(t, "")
	body, err := FirewallNftRules("gnb_tun")
	if err != nil {
		t.Fatal(err)
	}
	iAdd := strings.Index(body, "add table")
	iFlush := strings.Index(body, "flush table")
	if iAdd < 0 || iFlush < 0 || iAdd > iFlush {
		t.Fatalf("add table 必须在 flush table 之前:\n%s", body)
	}
}

// 持久化面选择：active 的守护进程优先 —— 它 reload 时重建自己的规则集，
// 登记错了等于没登记。
func TestSelectFirewallBackend(t *testing.T) {
	cases := []struct {
		name  string
		facts FirewallBackendFacts
		want  FirewallBackend
	}{
		{"firewalld 在管", FirewallBackendFacts{FirewalldActive: true, UfwActive: true}, FirewallBackendFirewalld},
		{"ufw 在管", FirewallBackendFacts{UfwActive: true, NetfilterPersistent: true}, FirewallBackendUfw},
		{"nftables 加载面可用", FirewallBackendFacts{NftablesUnitPresent: true, NftablesConfIncludesD: true}, FirewallBackendNftablesD},
		{"iptables-persistent", FirewallBackendFacts{NetfilterPersistent: true}, FirewallBackendIPtablesPersistent},
		{"都没有", FirewallBackendFacts{}, FirewallBackendNone},
		// 本机实测的反例：目录存在但没人 include → 不能算可用
		{"nftables.d 未被 include", FirewallBackendFacts{NftablesUnitPresent: true}, FirewallBackendNone},
	}
	for _, c := range cases {
		if got := selectFirewallBackend(c.facts); got != c.want {
			t.Fatalf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

// argv → nft 文件语法的转换契约：
//
//	· 去掉首词 nft（CLI 语法与 -f 文件语法对 add/flush 一致）
//	· iifname/oifname 后的设备名补双引号（文件语法裸标识会被当表达式）
//	· 非 nft 命令不进批次，原样留在 rest
func TestNftBatchFromCmds(t *testing.T) {
	cmds := []routeCmd{
		{argv: []string{"nft", "add", "table", "ip", "mynet_proxy"}},
		{argv: []string{"nft", "add", "chain", "ip", "mynet_proxy", "dns-redirect",
			"{ type nat hook prerouting priority -100; }"}},
		{argv: []string{"nft", "add", "rule", "ip", "mynet_proxy", "dns-redirect",
			"iifname", "gnb_tun", "oifname", "enp4s3", "masquerade"}},
		{argv: []string{"ip", "route", "replace", "default", "dev", "gnb_tun", "table", "200"}},
		{argv: []string{"sysctl", "-w", "net.ipv4.ip_forward=1"}},
	}
	batch, ok := NftBatchFromCmds(cmds)
	if !ok {
		t.Fatal("应识别出 nft 批次")
	}
	for _, want := range []string{
		"add table ip mynet_proxy",
		`add chain ip mynet_proxy dns-redirect { type nat hook prerouting priority -100; }`,
		`add rule ip mynet_proxy dns-redirect iifname "gnb_tun" oifname "enp4s3" masquerade`,
	} {
		if !strings.Contains(batch, want) {
			t.Fatalf("批次缺 %q:\n%s", want, batch)
		}
	}
	// 反不变量：不带引号的裸设备名不得出现（会被当表达式）
	if strings.Contains(batch, "iifname gnb_tun") {
		t.Fatalf("iifname 后的设备名必须带引号:\n%s", batch)
	}
	if strings.Contains(batch, "nft add") {
		t.Fatalf("批次里不得再带 `nft` 前缀:\n%s", batch)
	}

	// 非 nft 命令留 rest
	_, rest := SplitNftCmds(cmds)
	if len(rest) != 2 {
		t.Fatalf("rest 应为 2 条（ip route + sysctl），得到 %d: %+v", len(rest), rest)
	}
	// 全非 nft → 无批次
	if b, ok := NftBatchFromCmds(rest); ok || b != "" {
		t.Fatalf("无 nft 命令时不应有批次: %q %v", b, ok)
	}
}

// SplitNftCmds 的 batch 与 rest 必须**互补**：进 batch 的不进 rest，反之亦然。
func TestSplitNftCmdsPartition(t *testing.T) {
	cmds := []routeCmd{
		{argv: []string{"nft", "flush", "chain", "ip", "mynet_proxy", "PREROUTING"}},
		{argv: []string{"iptables", "-t", "nat", "-A", "OUTPUT", "-j", "ACCEPT"}},
		{argv: []string{"nft", "add", "rule", "ip", "mynet_proxy", "PREROUTING", "accept"}},
	}
	batch, rest := SplitNftCmds(cmds)
	if strings.Count(batch, "\n") < 2 {
		t.Fatalf("batch 应含两条:\n%s", batch)
	}
	if len(rest) != 1 || rest[0].argv[0] != "iptables" {
		t.Fatalf("rest 应只有 iptables 那条: %+v", rest)
	}
}

// open-port 也进产物：listen 端口 > 0 时 nft 文件里要有 input 链放行它。
// 反不变量：Go 侧不得再拼放行规则（见 TestFirewallArgvDoesNotComposeOpenPort）。
func TestFirewallNftRulesOpenPort(t *testing.T) {
	withWAN(t, "")
	prev := firewallListenPortResolver
	firewallListenPortResolver = func() int { return 9016 }
	t.Cleanup(func() { firewallListenPortResolver = prev })

	body, err := FirewallNftRules("gnb_tun")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"chain input {",
		"type filter hook input priority 0;",
		"udp dport 9016 accept",
		ListenPortComment,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("产物缺 %q:\n%s", want, body)
		}
	}

	// 端口未知 → 不放行（不猜端口）
	firewallListenPortResolver = func() int { return 0 }
	body, err = FirewallNftRules("gnb_tun")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "chain input {") {
		t.Fatalf("无 listen 端口时不得出 input 链:\n%s", body)
	}
}

// iptables 回退路径的幂等地基（结构锁）：规则进自有链、共享链只留守卫跳转。
// 三条缺一不可：
//
//	· `-N`/`-F` 自有链 —— 否则 `-A` 随 apply 累积（2026-10-17 实测 2→4→6）
//	· 跳转用 `-C || -I` 守卫 —— 否则共享链上跳转也会累积
//	· 不得直 `-A INPUT`/`-A OUTPUT`/`-A PREROUTING`（共享链）
func TestIptablesOwnChainHelpers(t *testing.T) {
	cmds := IptOwnChainCmds("nat", IPTablesChainDnsOut)
	if len(cmds) != 2 || !cmds[0].optional || !cmds[1].optional {
		t.Fatalf("own chain 应为 -N/-F 两条且 optional: %+v", cmds)
	}
	if got := strings.Join(cmds[0].argv, " "); got != "iptables -t nat -N MYNET_DNS_OUT" {
		t.Fatalf("-N 形状不符: %s", got)
	}

	j := IptJumpCmd("nat", "OUTPUT", IPTablesChainDnsOut)
	script := strings.Join(j.argv, " ")
	if j.argv[0] != "sh" || j.argv[1] != "-c" {
		t.Fatalf("跳转应走 sh -c（条件语义）: %v", j.argv)
	}
	if !strings.Contains(script, "iptables -t nat -C OUTPUT -j MYNET_DNS_OUT") ||
		!strings.Contains(script, "iptables -t nat -I OUTPUT 1 -j MYNET_DNS_OUT") ||
		!strings.Contains(script, "||") {
		t.Fatalf("跳转必须是 `-C || -I` 守卫式: %s", script)
	}

	u := IptUnjumpCmd("", "INPUT", IPTablesChainDohIn)
	if !u.optional || strings.Join(u.argv, " ") != "iptables -D INPUT -j MYNET_DOH_IN" {
		t.Fatalf("摘跳转形状不符: %+v", u)
	}
}

// 反不变量：iptables 分支不得再往**共享链**追加规则。
func TestIptablesFallbackOwnChainOnly(t *testing.T) {
	snap := &shared.SystemSnapshot{
		Capabilities: map[string]bool{"iptables": true},
		OS:           "linux",
	}
	groups := map[string][]routeCmd{
		"DoT block":   dohBlockCmds(snap),
		"DNS hijack":  DNSHijackCmds(snap, DNSHijackRuntime{Scope: DNSHijackLocal, Port: 5353}),
		"DNS cleanup": DNSHijackCleanupCmds(snap, DNSHijackRuntime{Scope: DNSHijackLocal, Port: 5353}),
	}
	sharedChains := []string{"INPUT", "FORWARD", "OUTPUT", "PREROUTING"}
	for name, cmds := range groups {
		for _, c := range cmds {
			joined := strings.Join(c.argv, " ")
			// sh -c 里的跳转守卫是唯一允许碰共享链的方式（-C/-I/-D），且只做跳转
			if len(c.argv) > 1 && c.argv[0] == "sh" && c.argv[1] == "-c" {
				if strings.Contains(joined, " -A ") {
					t.Fatalf("%s: sh -c 里不得出现 -A: %s", name, joined)
				}
				continue
			}
			if !strings.HasPrefix(joined, "iptables") {
				continue
			}
			for _, ch := range sharedChains {
				// 允许：-C/-I/-D 到共享链（跳转管理）；禁止：-A/-F 到共享链
				if strings.Contains(joined, " "+ch+" ") &&
					(strings.Contains(joined, " -A "+ch) || strings.Contains(joined, " -F "+ch)) {
					t.Fatalf("%s 不得直改共享链 %s: %s", name, ch, joined)
				}
			}
		}
	}
}

// ipset 幂等：必须 `-exist` 建集 + `flush` 清元素，否则重复 apply 会堆元素/报错。
func TestIPSetCmdsIdempotent(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"iptables": true}}
	cmds := proxyIPSetCmds(snap, &domain.ProxyPolicy{}, ProxyRuntime{TunDevice: "gnb_tun", IPSetFile: "/tmp/list.txt"}, 0xc8)
	joined := strings.Join(flattenCmds(cmds), " ")
	if !strings.Contains(joined, "ipset create mynet_proxy hash:net -exist") {
		t.Fatalf("ipset create 必须带 -exist: %s", joined)
	}
	if !strings.Contains(joined, "ipset flush mynet_proxy") {
		t.Fatalf("ipset 必须 flush（清旧元素再灌）: %s", joined)
	}
}
