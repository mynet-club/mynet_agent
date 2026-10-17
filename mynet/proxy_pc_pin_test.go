package mynet

import (
	"strings"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func pcSnap(osName string) *shared.SystemSnapshot {
	return &shared.SystemSnapshot{OS: osName, Capabilities: map[string]bool{}}
}

func TestPCProxyPinCmdsDarwinDialect(t *testing.T) {
	snap := pcSnap("darwin")
	cmds := PCProxyPinCmds(snap, []string{"1.2.3.4", "5.6.7.8", "not-an-ip"}, "utun6")
	// darwin 的 replace = 先删后加 → 每 IP 2 条；非法 IP 跳过。
	if len(cmds) != 4 {
		t.Fatalf("应生成 4 条（2 IP × 删+加；非法 IP 跳过），实际 %d", len(cmds))
	}
	text := joinCmds(cmds)
	for _, want := range []string{"1.2.3.4", "5.6.7.8", "utun6"} {
		if !strings.Contains(text, want) {
			t.Fatalf("darwin 方言缺 %s:\n%s", want, text)
		}
	}
}

func TestPCProxyPinNoopOnLinux(t *testing.T) {
	if cmds := PCProxyPinCmds(pcSnap("linux"), []string{"1.2.3.4"}, "gnb_tun"); len(cmds) != 0 {
		t.Fatalf("Linux 不得在主线钉主表路由: %v", cmds)
	}
}

func TestPCProxyPinDelOptional(t *testing.T) {
	cmds := PCProxyPinDelCmds(pcSnap("windows"), []string{"1.2.3.4"}, "P2PNet")
	if len(cmds) == 0 {
		t.Fatal("windows 应生成删除命令")
	}
	for _, c := range cmds {
		if !c.optional {
			t.Fatalf("删除命令应 optional（幂等）: %v", c.argv)
		}
	}
}

func TestResolvePCProxyPinIPsMergesAndDedups(t *testing.T) {
	rules := &domain.ProxyDomainRules{
		Proxied:       []string{"a.com"},
		CustomDomains: []string{"b.com"},
	}
	resolve := func(d string) []string {
		switch d {
		case "a.com":
			return []string{"1.1.1.1", "2.2.2.2"}
		case "b.com":
			return []string{"2.2.2.2", "3.3.3.3"} // 2.2.2.2 去重
		default:
			return []string{"9.9.9.9"}
		}
	}
	got := ResolvePCProxyPinIPs(rules, resolve, 0)
	want := "1.1.1.1,2.2.2.2,3.3.3.3"
	if strings.Join(got, ",") != want {
		t.Fatalf("解析集错误（基础清单 ∪ 自定义，去重）: %v", got)
	}
}

func TestResolvePCProxyPinExcludesDirect(t *testing.T) {
	// a.com 同时在 Proxied 与 CustomDirect → 直连优先，剔除，不解析。
	rules := &domain.ProxyDomainRules{Proxied: []string{"a.com"}, CustomDirect: []string{"a.com"}}
	resolve := func(string) []string { return []string{"1.1.1.1"} }
	if got := ResolvePCProxyPinIPs(rules, resolve, 0); len(got) != 0 {
		t.Fatalf("被自定义直连剔除的域名不应解析: %v", got)
	}
}

func TestResolvePCProxyPinIPsCap(t *testing.T) {
	rules := &domain.ProxyDomainRules{Proxied: []string{"x.com"}}
	resolve := func(string) []string { return []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} }
	if got := ResolvePCProxyPinIPs(rules, resolve, 2); len(got) != 2 {
		t.Fatalf("应封顶到 2，实际 %d", len(got))
	}
}

func TestResolvePCProxyPinIncludesCustomIPs(t *testing.T) {
	// 域名解析出 1.1.1.1；CustomIPs 直接并入 9.9.9.9；域名语法条目被拒。
	rules := &domain.ProxyDomainRules{
		Proxied:   []string{"a.com"},
		CustomIPs: []string{"9.9.9.9", "not-an-ip", "1.1.1.1"},
	}
	resolve := func(string) []string { return []string{"1.1.1.1"} }
	got := ResolvePCProxyPinIPs(rules, resolve, 0)
	want := "1.1.1.1,9.9.9.9"
	if strings.Join(got, ",") != want {
		t.Fatalf("CustomIPs 并集错误（去重+拒非法）: got %v want %s", got, want)
	}
}

func TestPCProxyPinNeverTouchesDefaultRoute(t *testing.T) {
	// 路由表安全不变量：钉扎方言只产出主机路由，绝无 default / 0.0.0.0。
	// darwin 用 `route -n -host <ip>`（裸 IP，不带 /32）；windows 用 `prefix=<ip>/32`。
	for _, osName := range []string{"darwin", "windows"} {
		cmds := PCProxyPinCmds(pcSnap(osName), []string{"1.2.3.4", "8.8.8.8"}, "utun4")
		text := joinCmds(cmds)
		if strings.Contains(text, "default") {
			t.Fatalf("%s: 钉扎不得含 default:\n%s", osName, text)
		}
		if strings.Contains(text, "0.0.0.0/0") {
			t.Fatalf("%s: 钉扎不得含 0.0.0.0/0:\n%s", osName, text)
		}
		if !strings.Contains(text, "1.2.3.4") {
			t.Fatalf("%s: 应含目标 IP:\n%s", osName, text)
		}
		switch osName {
		case "darwin":
			if !strings.Contains(text, "-host") {
				t.Fatalf("darwin 应为 -host 主机路由:\n%s", text)
			}
		case "windows":
			if !strings.Contains(text, "prefix=1.2.3.4/32") {
				t.Fatalf("windows 应为 prefix=/32:\n%s", text)
			}
		}
	}
}

func TestPCDNSCacheRoundTrip(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	in := map[string]pcDNSCacheEntry{
		"example.com": {IPs: []string{"1.2.3.4"}, ResolvedAt: time.Unix(1700000000, 0).UTC()},
	}
	if err := SavePCDNSCache(in); err != nil {
		t.Fatal(err)
	}
	out := LoadPCDNSCache()
	e, ok := out["example.com"]
	if !ok || len(e.IPs) != 1 || e.IPs[0] != "1.2.3.4" {
		t.Fatalf("缓存回读失败: %+v", out)
	}
}

func TestResolveDomainCleanUsesCache(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	now := time.Now()
	// 预置未过期缓存 → 不应触发 resolve（上游不可达也无所谓）。
	if err := SavePCDNSCache(map[string]pcDNSCacheEntry{
		"cached.example": {IPs: []string{"5.6.7.8"}, ResolvedAt: now.Add(-time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	rules := &domain.ProxyDomainRules{TunnelServer: "10.0.0.1", ExitDNS: "10.0.0.2"}
	got := ResolveDomainClean(rules, "cached.example", now)
	if len(got) != 1 || got[0] != "5.6.7.8" {
		t.Fatalf("TTL 命中应返回缓存: %v", got)
	}
	// 过期缓存 + 无可达上游 → fail-open 沿用旧 IP。
	if err := SavePCDNSCache(map[string]pcDNSCacheEntry{
		"stale.example": {IPs: []string{"9.9.9.9"}, ResolvedAt: now.Add(-2 * time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	// 上游用不可路由地址，超时后应沿用旧缓存。
	rulesBad := &domain.ProxyDomainRules{TunnelServer: "127.0.0.1"}
	got2 := ResolveDomainClean(rulesBad, "stale.example", now)
	if len(got2) != 1 || got2[0] != "9.9.9.9" {
		t.Fatalf("上游失败应 fail-open 沿用旧缓存: %v", got2)
	}
}

func TestCleanDNSUpstreamOrder(t *testing.T) {
	rules := &domain.ProxyDomainRules{TunnelServer: "10.1.1.1", ExitDNS: "10.2.2.2"}
	ups := CleanDNSUpstreams(rules)
	if len(ups) < 2 || ups[0] != "10.1.1.1" || ups[1] != "10.2.2.2" {
		t.Fatalf("上游顺序应 TunnelServer > ExitDNS: %v", ups)
	}
}

func TestMergeIPsToCIDRMinimal(t *testing.T) {
	// 相邻成对 → /31；四个连续 → /30；不相邻保持 /32；绝不扩大覆盖
	got := MergeIPsToCIDR([]string{"1.2.3.4", "1.2.3.5", "1.2.3.6", "1.2.3.7", "9.9.9.9"})
	want := "1.2.3.4/30,9.9.9.9/32"
	if strings.Join(got, ",") != want {
		t.Fatalf("merge: got %v want %s", got, want)
	}
	// 半对齐：.5+.6 不能合成一个块（缺 .4/.7），保持两个 /32
	got2 := MergeIPsToCIDR([]string{"1.2.3.5", "1.2.3.6"})
	if strings.Join(got2, ",") != "1.2.3.5/32,1.2.3.6/32" {
		t.Fatalf("非对齐不合并: %v", got2)
	}
	// 空
	if MergeIPsToCIDR(nil) != nil {
		t.Fatal("空集应 nil")
	}
}

func TestHookDomainStarMatch(t *testing.T) {
	// *.example.com 归一化后 = example.com，后缀匹配子域
	r := &dnsHookRules{
		proxied: map[string]bool{"example.com": true},
		clean:   map[string]bool{},
	}
	if !r.shouldPin("example.com") || !r.shouldPin("a.example.com") || !r.shouldPin("a.b.example.com") {
		t.Fatal("*.example.com 应匹配自身与子域")
	}
	if r.shouldPin("notexample.com") {
		t.Fatal("后缀误匹配")
	}
}

func TestApplyPCPinReconciles(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	if err := SavePCPinIPs([]string{"1.1.1.1", "2.2.2.2"}); err != nil {
		t.Fatal(err)
	}
	rules := &domain.ProxyDomainRules{Proxied: []string{"x.com"}}
	// 用固定 resolve 不行（ApplyPCPin 内部走系统解析器）；直接测对账核心：
	prev := LoadPCPinIPs()
	next := []string{"2.2.2.2", "3.3.3.3"}
	rm := diffStrings(prev, next)
	if strings.Join(rm, ",") != "1.1.1.1" {
		t.Fatalf("应撤 1.1.1.1（失效），实际 %v", rm)
	}
	_ = rules
}

// CIDR 感知：IP 落在已钉前缀内必须判「已覆盖」，不再补装路由。
func TestIPCoveredByPinCIDRs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	// 钉 10.1.2.0/24（合并 256 个 /32 太重，直接写一条 CIDR 到 pin 文件）
	if err := SavePCPinIPs([]string{"10.1.2.0/24"}); err != nil {
		t.Fatal(err)
	}
	if !ipCoveredByPinCIDRs("10.1.2.5") {
		t.Fatal("10.1.2.5 应被 10.1.2.0/24 覆盖")
	}
	if ipCoveredByPinCIDRs("10.1.3.5") {
		t.Fatal("10.1.3.5 不在 10.1.2.0/24 内，不得判覆盖")
	}
	if ipCoveredByPinCIDRs("not-an-ip") {
		t.Fatal("非法 IP 必须判未覆盖（保守补装）")
	}
}

// dns-hook-pins 与 pc-pin 两份清单都要参与覆盖判定。
func TestIPCoveredByHookPinsToo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	s := &dnsHookPinState{IPs: map[string]time.Time{"192.168.50.7": time.Now()}}
	s.persistLocked()
	if !ipCoveredByPinCIDRs("192.168.50.7") {
		t.Fatal("dns-hook-pins 里的 IP 应被覆盖判定认到")
	}
	if ipCoveredByPinCIDRs("192.168.50.8") {
		t.Fatal("相邻 IP 未钉扎，不得判覆盖")
	}
}
