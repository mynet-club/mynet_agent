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
