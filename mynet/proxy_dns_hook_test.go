package mynet

import (
	"strings"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func TestDynamicIPSetAddAnswer(t *testing.T) {
	s := NewDynamicIPSet(time.Minute)
	now := time.Now()
	added := s.AddAnswer("api.github.com", []string{"1.2.3.4", "5.6.7.8", "not-an-ip"}, now)
	if len(added) != 2 {
		t.Fatalf("added=%v", added)
	}
	// 重复不报新增
	added2 := s.AddAnswer("api.github.com", []string{"1.2.3.4"}, now)
	if len(added2) != 0 {
		t.Fatalf("重复应无新增: %v", added2)
	}
	if got := s.IPs(); len(got) != 2 {
		t.Fatalf("IPs=%v", got)
	}
}

func TestDynamicIPSetExpire(t *testing.T) {
	s := NewDynamicIPSet(time.Second)
	now := time.Now()
	s.AddAnswer("x.com", []string{"10.0.0.1"}, now)
	dead := s.Expire(now.Add(2 * time.Second))
	if len(dead) != 1 || dead[0] != "10.0.0.1" {
		t.Fatalf("dead=%v", dead)
	}
	if len(s.IPs()) != 0 {
		t.Fatal("过期后应为空")
	}
}

func TestDynamicIPSetCmdsIncrementalNotFlush(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true}}
	cmds := DynamicIPSetCmds(snap, []string{"1.2.3.4", "5.6.7.8"}, "", "", "")
	if len(cmds) != 2 {
		t.Fatalf("cmds=%v", cmds)
	}
	text := joinCmds(cmds)
	if strings.Contains(text, "flush") {
		t.Fatalf("动态增量禁止 flush:\n%s", text)
	}
	for _, want := range []string{
		"nft add element ip mynet_proxy dst4 { 1.2.3.4 }",
		"nft add element ip mynet_proxy dst4 { 5.6.7.8 }",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q\n%s", want, text)
		}
	}
}

func TestDynamicIPSetCmdsIptables(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"iptables": true}}
	cmds := DynamicIPSetCmds(snap, []string{"9.9.9.9"}, "", "", "")
	text := joinCmds(cmds)
	if !strings.Contains(text, "ipset add mynet_proxy 9.9.9.9 -exist") {
		t.Fatalf("text=%s", text)
	}
}

func TestDynamicIPSetDelCmds(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true}}
	cmds := DynamicIPSetDelCmds(snap, []string{"1.1.1.1"}, "", "", "")
	text := joinCmds(cmds)
	if !strings.Contains(text, "nft delete element ip mynet_proxy dst4 { 1.1.1.1 }") {
		t.Fatalf("text=%s", text)
	}
	for _, c := range cmds {
		if !c.optional {
			t.Fatal("del 应 optional")
		}
	}
}

func TestDnsmasqIPSetLines(t *testing.T) {
	lines := DnsmasqIPSetLines([]string{".github.com", "google.com", ".github.com", ""}, "mynet_proxy")
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "ipset=/github.com/mynet_proxy") {
		t.Fatalf("text=%s", text)
	}
	if !strings.Contains(text, "ipset=/google.com/mynet_proxy") {
		t.Fatalf("text=%s", text)
	}
	if strings.Count(text, "github.com") != 1 {
		t.Fatalf("应去重:\n%s", text)
	}
}

func TestDoHBlockCmds(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true}}
	cmds := DoHBypassCmds(snap, DoHModeBlock, ProxyRuntime{})
	text := joinCmds(cmds)
	for _, want := range []string{
		"tcp dport 853 drop",
		"ip daddr 8.8.8.8 tcp dport 443 drop",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("block 缺 %q\n%s", want, text)
		}
	}
	// allow 不得产生命令
	if got := DoHBypassCmds(snap, DoHModeAllow, ProxyRuntime{}); len(got) != 0 {
		t.Fatalf("allow 应空: %v", got)
	}
}

func TestDoHTunnelCmds(t *testing.T) {
	cmds := DoHBypassCmds(nil, DoHModeTunnel, ProxyRuntime{TunDevice: "gnb_tun"})
	// snap nil 时 tunnel 仍应有路由（不依赖 nft）
	text := joinCmds(cmds)
	if !strings.Contains(text, "ip route replace 8.8.8.8/32 dev gnb_tun") { // GUARD-OK 测试断言命令串，非执行点
		t.Fatalf("tunnel 模式应钉 /32:\n%s", text)
	}
}

func TestFullTunnelCmds(t *testing.T) {
	// FullTunnel 仅 Linux：必须带 ip 能力的快照（#21：BSD 快照下返回空）
	cmds := FullTunnelCmds(snapNft(), ProxyRuntime{
		TunDevice:  "gnb_tun",
		WANDevice:  "eth0",
		WANGateway: "192.0.2.1",
	}, 200)
	text := joinCmds(cmds)
	if !strings.Contains(text, "ip route replace default dev gnb_tun") { // GUARD-OK 测试断言命令串，非执行点
		t.Fatalf("应改主表默认:\n%s", text)
	}
	if !strings.Contains(text, "192.0.2.1/32 dev eth0") {
		t.Fatal("WAN /32 修复")
	}
	if got := FullTunnelCmds(&shared.SystemSnapshot{OS: "darwin", Capabilities: map[string]bool{}},
		ProxyRuntime{TunDevice: "gnb_tun"}, 200); len(got) != 0 {
		t.Fatalf("BSD 下 FullTunnel 必须为空（不得碰主表 default）: %v", got)
	}
}

func TestProxyAllBypassCombines(t *testing.T) {
	snap := snapNft() // 需要 ip 能力：full tunnel 是 Linux 独有（#21 门禁）
	p := &domain.ProxyPolicy{
		Enabled:         true,
		Role:            "client",
		ForeignDNSRoute: true,
		FullTunnel:      true,
		DoHMode:         DoHModeBlock,
	}
	rt := ProxyRuntime{TunDevice: "gnb_tun"}
	cmds := ProxyAllBypassCmds(snap, p, rt)
	text := joinCmds(cmds)
	if !strings.Contains(text, "dport 853 drop") {
		t.Fatalf("应含 DoH block:\n%s", text)
	}
	if !strings.Contains(text, "ip route replace default dev gnb_tun") { // GUARD-OK 测试断言命令串，非执行点
		t.Fatalf("应含 full tunnel:\n%s", text)
	}
}

func TestProxyACLCmdsServerAllowCIDR(t *testing.T) {
	snap := &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true}}
	p := &domain.ProxyPolicy{
		Enabled: true,
		Role:    "server",
		ACL: &domain.ProxyACL{
			Mode:         domain.ProxyACLAllowlist,
			AllowedCIDRs: []string{"10.20.0.0/24"},
		},
	}
	cmds := ProxyACLCmds(snap, p, ProxyRuntime{TunDevice: "gnb_tun"})
	text := joinCmds(cmds)
	if !strings.Contains(text, "ip saddr 10.20.0.0/24 accept") {
		t.Fatalf("缺放行:\n%s", text)
	}
	if !strings.Contains(text, "iifname gnb_tun drop") {
		t.Fatalf("缺默认拒:\n%s", text)
	}
	// client 不装
	c := *p
	c.Role = "client"
	if got := ProxyACLCmds(snap, &c, ProxyRuntime{TunDevice: "gnb_tun"}); len(got) != 0 {
		t.Fatalf("client 不该装 ACL 准入: %v", got)
	}
}
