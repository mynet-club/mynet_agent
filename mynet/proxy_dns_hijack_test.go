package mynet

import (
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared"
)

func TestDNSHijackLocalPC(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true}}
	cmds := DNSHijackCmds(snap, DNSHijackRuntime{Scope: DNSHijackLocal})
	text := joinCmds(cmds)
	for _, want := range []string{
		// 防自环：放行 dnsmasq 自己的上游查询
		"meta skuid dnsmasq udp dport 53 accept",
		"ip daddr 127.0.0.1 udp dport 53 accept",
		// 本机劫持
		"nft add rule ip mynet_proxy dns-hijack-out udp dport 53 redirect to :53",
		"nft add rule ip mynet_proxy dns-hijack-out tcp dport 53 redirect to :53",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("local 劫持缺 %q\n%s", want, text)
		}
	}
	// PC 不该劫 LAN
	if strings.Contains(text, "PREROUTING") {
		t.Fatalf("local 不得写 PREROUTING:\n%s", text)
	}
}

func TestDNSHijackLANGateway(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true}}
	cmds := DNSHijackCmds(snap, DNSHijackRuntime{Scope: DNSHijackLAN, LANBridge: "br-lan"})
	text := joinCmds(cmds)
	for _, want := range []string{
		"iifname br-lan udp dport 53 redirect to :53",
		"iifname br-lan tcp dport 53 redirect to :53",
		"nft add rule ip mynet_proxy dns-hijack-out udp dport 53 redirect to :53",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("lan 劫持缺 %q\n%s", want, text)
		}
	}
}

func TestDNSHijackOffNil(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true}}
	if got := DNSHijackCmds(snap, DNSHijackRuntime{Scope: DNSHijackOff}); got != nil {
		t.Fatalf("off 应为空: %v", got)
	}
	if got := DNSHijackCmds(snap, DNSHijackRuntime{}); got != nil {
		t.Fatalf("空 scope 应为空: %v", got)
	}
}

func TestDNSHijackIptablesFallback(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"iptables": true}}
	cmds := DNSHijackCmds(snap, DNSHijackRuntime{Scope: DNSHijackLAN, LANBridge: "eth0", ResolverUser: "dnsmasq"})
	text := joinCmds(cmds)
	// 幂等形态：规则进**自有链**，共享链只留守卫跳转（-C || -I）。
	for _, want := range []string{
		"iptables -t nat -N " + IPTablesChainDnsOut, // 自有链（-N 已存在跳过）
		"iptables -t nat -F " + IPTablesChainDnsOut, // 清空后重加
		"iptables -t nat -A " + IPTablesChainDnsOut + " -p udp --dport 53 -m owner --uid-owner dnsmasq -j ACCEPT",
		"iptables -t nat -A " + IPTablesChainDnsOut + " -p udp --dport 53 -j REDIRECT --to-ports 53",
		"iptables -t nat -A " + IPTablesChainDnsPre + " -i eth0 -p udp --dport 53 -j REDIRECT --to-ports 53",
		"iptables -t nat -C OUTPUT -j " + IPTablesChainDnsOut, // 守卫：查到即跳过
		"iptables -t nat -I OUTPUT 1 -j " + IPTablesChainDnsOut,
		"iptables -t nat -C PREROUTING -j " + IPTablesChainDnsPre,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("iptables 劫持缺 %q\n%s", want, text)
		}
	}
	// 反不变量：不得直接往共享链追加规则
	for _, bad := range []string{
		"iptables -t nat -A OUTPUT ",
		"iptables -t nat -A PREROUTING ",
	} {
		if strings.Contains(text, bad) {
			t.Fatalf("不得直改共享链（%q）:\n%s", bad, text)
		}
	}
}

func TestDNSHijackCleanup(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true}}
	cmds := DNSHijackCleanupCmds(snap, DNSHijackRuntime{Scope: DNSHijackLocal})
	if len(cmds) == 0 {
		t.Fatal("cleanup 为空")
	}
	for _, c := range cmds {
		if !c.optional {
			t.Fatalf("cleanup 应 optional: %v", c.argv)
		}
	}
}

// 防自环是硬性质：redirect 之前必须有 skuid 放行。
func TestHijackAcceptsBeforeRedirect(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true}}
	cmds := DNSHijackCmds(snap, DNSHijackRuntime{Scope: DNSHijackLocal})
	acc, red := -1, -1
	for i, c := range cmds {
		s := strings.Join(c.argv, " ")
		if acc < 0 && strings.Contains(s, "skuid") && strings.Contains(s, "accept") {
			acc = i
		}
		// 链名现在是 dns-hijack-out（自有表），不是 `ip nat` 的 OUTPUT
		if red < 0 && strings.Contains(s, "redirect to :53") && strings.Contains(s, ProxyChainHijackOut) {
			red = i
		}
	}
	if acc < 0 || red < 0 || acc > red {
		t.Fatalf("skuid 放行必须在 redirect 之前: acc=%d red=%d", acc, red)
	}
}
