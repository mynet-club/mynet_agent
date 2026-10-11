package mynet

import (
	"strings"
	"testing"

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
