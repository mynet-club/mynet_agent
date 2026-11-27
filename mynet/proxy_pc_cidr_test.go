package mynet

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

// TestPCSessionCustomIPsCIDRNotDroppedByParseIP 回归护栏（PRD §9）：
// custom_ips 与 presetIPs 含 CIDR 时必须端到端进入路由集合，不得被 net.ParseIP 静默丢弃。
func TestPCSessionCustomIPsCIDRNotDroppedByParseIP(t *testing.T) {
	h := newSessionHarness(t)
	h.s.localNets = func() []*net.IPNet { return nil }
	now := time.Now()
	r := sessionRules("gemini.google.com")
	r.presetIPs = []string{"142.250.0.0/15"}
	r.customIPs = []string{"203.0.113.9/24"}

	if err := h.s.sync(r, "utun4", now); err != nil {
		t.Fatal(err)
	}
	if h.fib["142.250.0.0/15"] != "utun4" || h.fib["203.0.113.0/24"] != "utun4" {
		t.Fatalf("CIDR prefixes not installed into FIB: %v", h.fib)
	}

	// 删除 custom_ips 后，对应 CIDR 路由撤销，预设仍保留
	r.customIPs = nil
	if err := h.s.sync(r, "utun4", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.fib["203.0.113.0/24"]; ok {
		t.Fatalf("removed custom CIDR still in FIB: %v", h.fib)
	}
	if h.fib["142.250.0.0/15"] != "utun4" {
		t.Fatalf("preset CIDR unexpectedly removed: %v", h.fib)
	}

	// 关闭加速（release）后，预设与自定义路由全部撤销
	if err := h.s.release(now.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(h.fib) != 1 || len(h.s.Routes) != 0 {
		t.Fatalf("release left routes behind: fib=%v routes=%v", h.fib, h.s.Routes)
	}
}

// TestPCSessionExplicitCIDRSurvivesDNSTTLExpiryAndSharedCDN 验证：
// 1. 域名 pin 的 /32 落在显式前缀内，TTL 过期后显式前缀路由保持不断；
// 2. 显式前缀内的 IP 不受 markSharedIfPinned 共享 CDN 降级影响。
func TestPCSessionExplicitCIDRSurvivesDNSTTLExpiryAndSharedCDN(t *testing.T) {
	h := newSessionHarness(t)
	h.s.localNets = func() []*net.IPNet { return nil }
	now := time.Now()
	r := sessionRules("gemini.google.com")
	r.customIPs = []string{"142.250.0.0/15"}

	// 先装显式前缀，再观察域名解析落在前缀内的 IP
	mustObserve(t, h, r, "gemini.google.com", now, "142.250.4.188", "198.51.100.7")
	if h.fib["142.250.0.0/15"] != "utun4" {
		t.Fatalf("explicit CIDR missing: %v", h.fib)
	}
	if _, dup := h.fib["142.250.4.188/32"]; dup {
		t.Fatalf("redundant /32 installed inside explicit CIDR: %v", h.fib)
	}
	if h.fib["198.51.100.7/32"] != "utun4" {
		t.Fatalf("uncovered dynamic IP missing: %v", h.fib)
	}

	// 超过 DNS TTL 后再次同步：动态 IP 过期撤销，但 142.250.0.0/15 仍稳固在隧道口
	if err := h.s.sync(r, "utun4", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if h.fib["142.250.0.0/15"] != "utun4" {
		t.Fatalf("explicit CIDR lost after DNS TTL expiry: %v", h.fib)
	}
	if _, ok := h.fib["198.51.100.7/32"]; ok {
		t.Fatalf("expired dynamic IP remained: %v", h.fib)
	}
}

// TestPCSessionExplicitCIDRCapacityLimit513 验证超 512 条前缀时报错且不驱逐已有条目。
func TestPCSessionExplicitCIDRCapacityLimit513(t *testing.T) {
	h := newSessionHarness(t)
	h.s.localNets = func() []*net.IPNet { return nil }
	now := time.Now()
	r := sessionRules()
	r.customIPs = []string{"203.0.113.0/24"}
	if err := h.s.sync(r, "utun4", now); err != nil {
		t.Fatal(err)
	}

	var over []string
	for i := 0; i < pcPinMaxIPs+1; i++ {
		over = append(over, fmt.Sprintf("198.18.%d.%d/32", i/256, i%256))
	}
	r.customIPs = over
	if err := h.s.sync(r, "utun4", now.Add(time.Second)); err == nil {
		t.Fatal("expected capacity error for 513 entries")
	}
	if h.fib["203.0.113.0/24"] != "utun4" {
		t.Fatalf("existing route was evicted on capacity error: %v", h.fib)
	}
}

// TestPCSessionExplicitCIDRSkipsLocalSubnetAndRollsBackOnFailure 验证：
// 1. 与本机物理直连网段重叠的 CIDR 自动跳过；
// 2. 新增多条前缀中途失败时回滚本轮新增并保留上一代路由。
func TestPCSessionExplicitCIDRSkipsLocalSubnetAndRollsBackOnFailure(t *testing.T) {
	h := newSessionHarness(t)
	_, lanNet, _ := net.ParseCIDR("192.168.1.0/24")
	h.s.localNets = func() []*net.IPNet { return []*net.IPNet{lanNet} }
	now := time.Now()

	// 先装上一代路由 203.0.113.0/24，并验证 192.168.1.0/24 被跳过
	r := sessionRules()
	r.customIPs = []string{"203.0.113.0/24", "192.168.1.0/24"}
	if err := h.s.sync(r, "utun4", now); err != nil {
		t.Fatal(err)
	}
	if h.fib["203.0.113.0/24"] != "utun4" {
		t.Fatalf("expected 203.0.113.0/24 installed: %v", h.fib)
	}
	if _, ok := h.fib["192.168.1.0/24"]; ok {
		t.Fatalf("local LAN prefix must be skipped: %v", h.fib)
	}

	// 切换为两条新前缀，令第 2 条 route add 失败 → 第 1 条被回滚，上一代 203.0.113.0/24 仍保留
	baseRoute := h.s.route
	addCount := 0
	h.s.route = func(action, prefix, tun string) error {
		if action == "add" {
			addCount++
			if addCount == 2 {
				return errors.New("injected route add failure")
			}
		}
		return baseRoute(action, prefix, tun)
	}
	r.customIPs = []string{"142.250.0.0/15", "172.253.0.0/16"}
	if err := h.s.sync(r, "utun4", now.Add(time.Minute)); err == nil {
		t.Fatal("expected sync failure on 2nd route add")
	}
	if _, ok := h.fib["142.250.0.0/15"]; ok {
		t.Fatalf("1st newly added prefix should have been rolled back: %v", h.fib)
	}
	if h.fib["203.0.113.0/24"] != "utun4" {
		t.Fatalf("previous generation route must be preserved on failure: %v", h.fib)
	}
}

func TestParseProxyIPListBaseline(t *testing.T) {
	raw := `
# Google ranges
142.250.0.0/15   # main
142.250.4.188    # covered by /15
172.253.1.2/16
`
	got, err := parseProxyIPList(raw)
	if err != nil {
		t.Fatalf("parseProxyIPList failed: %v", err)
	}
	if len(got) != 2 || got[0] != "142.250.0.0/15" || got[1] != "172.253.0.0/16" {
		t.Fatalf("unexpected parsed proxy IP list: %v", got)
	}
	if _, err := parseProxyIPList("142.250.0.0/15\n0.0.0.0/1\n"); err == nil {
		t.Fatal("expected error on /1 in proxy-ips.txt")
	}
}

func TestPluginDomainsIPCLICIDRAndEquivalentRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	// 初始化最小 local intent 以便 activePluginBase 工作
	if rc := pluginDomains([]string{"ip", "add", "bad-domain.com"}); rc == 0 {
		t.Fatal("invalid domain input must return non-zero")
	}
	if rc := pluginDomains([]string{"ip", "add", "2001:db8::/32"}); rc == 0 {
		t.Fatal("IPv6 input must return non-zero")
	}
	if rc := pluginDomains([]string{"ip", "add", "0.0.0.0/1"}); rc == 0 {
		t.Fatal("too-wide /1 CIDR must return non-zero")
	}
	if rc := pluginDomains([]string{"ip", "add", "203.0.113.9/24"}); rc != 0 {
		t.Fatalf("add valid CIDR failed: rc=%d", rc)
	}
	if rc := pluginDomains([]string{"ip", "add", "1.2.3.4"}); rc != 0 {
		t.Fatalf("add single IP failed: rc=%d", rc)
	}
	// 验证等价删除：1.2.3.4/32 删掉 1.2.3.4，203.0.113.55/24 删掉 203.0.113.0/24
	if rc := pluginDomains([]string{"ip", "rm", "1.2.3.4/32"}); rc != 0 {
		t.Fatalf("rm equivalent /32 failed: rc=%d", rc)
	}
	if rc := pluginDomains([]string{"ip", "rm", "203.0.113.55/24"}); rc != 0 {
		t.Fatalf("rm equivalent CIDR failed: rc=%d", rc)
	}
	p := EffectiveProxyPolicy()
	if p != nil && p.DomainRules != nil && len(p.DomainRules.CustomIPs) != 0 {
		t.Fatalf("expected empty CustomIPs after equivalent removals, got %v", p.DomainRules.CustomIPs)
	}
}
