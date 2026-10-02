package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResolveProxySplitMatrix(t *testing.T) {
	cases := []struct {
		region, policy, list, mode string
	}{
		{"domestic", "whitelist", "international", "normal"},
		{"domestic", "blacklist", "china", "inverted"},
		{"international", "whitelist", "china", "normal"},
		{"international", "blacklist", "international", "inverted"},
	}
	for _, c := range cases {
		list, mode, err := ResolveProxySplit(c.region, c.policy)
		if err != nil {
			t.Fatalf("%s+%s: %v", c.region, c.policy, err)
		}
		if list != c.list || mode != c.mode {
			t.Fatalf("%s+%s => %s/%s, 期望 %s/%s", c.region, c.policy, list, mode, c.list, c.mode)
		}
	}
}

func TestResolveProxySplitRejects(t *testing.T) {
	for _, c := range [][2]string{
		{"", "whitelist"},
		{"domestic", ""},
		{"non_domestic", "blacklist"}, // 旧线废弃值，不再接受
		{"domestic", "graylist"},
	} {
		if _, _, err := ResolveProxySplit(c[0], c[1]); err == nil {
			t.Fatalf("应拒绝 %q %q", c[0], c[1])
		}
	}
}

func TestProxyPolicyValidateAndDefaults(t *testing.T) {
	src, mode, err := ResolveProxySplit(ProxyRegionDomestic, ProxyPolicyWhitelist)
	if err != nil {
		t.Fatal(err)
	}
	p := &ProxyPolicy{
		Enabled: true,
		Role:    "client",
		Region:  ProxyRegionDomestic,
		Policy:  ProxyPolicyWhitelist,
		Split: &ProxySplit{
			ListSource: src,
			MatchMode:  mode,
			IPList: &ProxyIPListRef{
				Source: src,
				URL:    "https://ctl.example/proxy-lists/interip.txt",
				SHA256: "ab",
			},
		},
		Exits:           []ProxyExit{{NodeID: "9001", Enabled: true}},
		DNS:             &ProxyDNS{Mode: "split", Server: "8.8.8.8", DomesticServers: []string{"223.5.5.5"}},
		ForeignDNSRoute: true,
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("合法策略被拒: %v", err)
	}
	if p.EffectiveFwmark() != 0xc8 || p.EffectiveTable() != 200 {
		t.Fatalf("默认 fwmark/table 不对: %#x %d", p.EffectiveFwmark(), p.EffectiveTable())
	}
	if !p.IsClientLike() || p.IsServerLike() {
		t.Fatal("client 角色判定错")
	}

	// 手写错 split 必须被拦住
	p.Split.ListSource = ProxyListChina
	p.Split.MatchMode = ProxyMatchInverted
	if err := p.Validate(); err == nil {
		t.Fatal("split 与矩阵不一致时必须报错")
	}
}

func TestProxyPolicyJSONRoundTrip(t *testing.T) {
	src, mode, _ := ResolveProxySplit(ProxyRegionIntl, ProxyPolicyBlacklist)
	in := &ProxyPolicy{
		Enabled: true,
		Role:    "both",
		Region:  ProxyRegionIntl,
		Policy:  ProxyPolicyBlacklist,
		Split: &ProxySplit{
			ListSource: src,
			MatchMode:  mode,
			IPList:     &ProxyIPListRef{Source: src, URL: "https://x/interip.txt", SHA256: "aa", Bytes: 10},
		},
		Exits: []ProxyExit{{NodeID: "9001", Weight: 20, Order: 1, Enabled: true}},
		DNS: &ProxyDNS{
			Mode:            "redirect",
			RedirectTarget:  "10.20.0.250",
			DomesticServers: []string{"223.5.5.5", "119.29.29.29"},
		},
		ForeignDNSRoute: true,
		Fwmark:          0xc8,
		Table:           200,
	}
	env := &BundleEnvelope{
		SchemaVersion: 2,
		Generation:    "rev-test",
		Checksum:      "sha256:x",
		GeneratedAt:   "2026-09-27T00:00:00Z",
		Node:          NodeRef{ID: "1001"},
		Driver:        DriverTypeGNB,
		Network:       NetworkIntent{Addresses: []string{"10.20.0.1/24"}, Proxy: in},
	}
	raw, err := EncodeBundleEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeBundleEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.Network.Proxy == nil {
		t.Fatal("proxy 段未解出")
	}
	got := out.Network.Proxy
	if got.Split == nil || got.Split.ListSource != src || got.Split.MatchMode != mode {
		t.Fatalf("split 往返丢失: %#v", got.Split)
	}
	if len(got.Exits) != 1 || got.Exits[0].NodeID != "9001" {
		t.Fatalf("exits 往返丢失: %#v", got.Exits)
	}
	if got.DNS == nil || got.DNS.Mode != "redirect" || got.DNS.RedirectTarget != "10.20.0.250" {
		t.Fatalf("dns 往返丢失: %#v", got.DNS)
	}
	// 编码里字段名稳定（双端契约）
	text := string(raw)
	for _, part := range []string{`"proxy"`, `"list_source"`, `"match_mode"`, `"foreign_dns_route"`} {
		if !json.Valid(raw) || !strings.Contains(text, part) {
			t.Fatalf("JSON 形状不对（缺 %s）: %s", part, text)
		}
	}
}

// DNS 三选一（spec §13.1）：模式 → nameserver 列表，顺序即优先级。
func TestProxyDNSNameservers(t *testing.T) {
	peer := []DNSServer{
		{Source: "friend", Addr: "10.182.236.180", NodeID: "4997342800601732"},
		{Source: "self", Addr: "10.182.236.181"},
	}
	smart := []DNSServer{
		{Source: "platform", Addr: "dns.mynet.club"},
		{Source: "self", Addr: "223.5.5.5"},
	}

	// off / nil → 不碰 DNS
	if got := (*ProxyDNS)(nil).Nameservers(); got != nil {
		t.Fatalf("nil 应返回 nil: %v", got)
	}
	if got := (&ProxyDNS{Mode: DNSModeOff}).Nameservers(); got != nil {
		t.Fatalf("off 应返回 nil: %v", got)
	}

	// peer → 只有 peer
	got := (&ProxyDNS{Mode: DNSModePeer, PeerServers: peer}).Nameservers()
	if len(got) != 2 || got[0] != "10.182.236.180" || got[1] != "10.182.236.181" {
		t.Fatalf("peer: %v", got)
	}

	// smart → 只有 smart
	got = (&ProxyDNS{Mode: DNSModeSmart, SmartServers: smart}).Nameservers()
	if len(got) != 2 || got[0] != "dns.mynet.club" || got[1] != "223.5.5.5" {
		t.Fatalf("smart: %v", got)
	}

	// peer_smart → peer 在前、smart 在后（顺序即优先级）
	got = (&ProxyDNS{Mode: DNSModePeerSmart, PeerServers: peer, SmartServers: smart}).Nameservers()
	want := []string{"10.182.236.180", "10.182.236.181", "dns.mynet.club", "223.5.5.5"}
	if len(got) != len(want) {
		t.Fatalf("peer_smart 长度 %d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("peer_smart[%d]=%s want %s", i, got[i], want[i])
		}
	}
}

func TestProxyDNSActive(t *testing.T) {
	if (*ProxyDNS)(nil).Active() {
		t.Fatal("nil 不应 Active")
	}
	if (&ProxyDNS{Mode: DNSModeOff}).Active() {
		t.Fatal("off 不应 Active")
	}
	if (&ProxyDNS{Mode: DNSModePeer}).Active() {
		t.Fatal("peer 无 server 不应 Active")
	}
	if !(&ProxyDNS{Mode: DNSModePeer, PeerServers: []DNSServer{{Addr: "10.0.0.1"}}}).Active() {
		t.Fatal("peer 有 server 应 Active")
	}
	// 旧模式不走三选一
	if (&ProxyDNS{Mode: "resolv", Server: "8.8.8.8"}).Active() {
		t.Fatal("旧模式不应算三选一 Active")
	}
}
