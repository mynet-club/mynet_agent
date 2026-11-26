package domain

import (
	"reflect"
	"testing"
)

func TestParseIPv4CIDRAndNormalize(t *testing.T) {
	// 1. 规范化单 IP 与含主机位的 CIDR
	cases := []struct {
		in   string
		want string
	}{
		{" 1.2.3.4 ", "1.2.3.4/32"},
		{"1.2.3.4/32", "1.2.3.4/32"},
		{"1.2.3.4/24", "1.2.3.0/24"},
		{"142.250.4.188/15", "142.250.0.0/15"},
		{"8.8.8.8/8", "8.0.0.0/8"},
	}
	for _, tc := range cases {
		got, err := ParseIPv4CIDR(tc.in)
		if err != nil {
			t.Fatalf("ParseIPv4CIDR(%q) unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseIPv4CIDR(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// 2. 拒绝非法、IPv6、越界前缀、保留网段
	invalid := []string{
		"",
		"   ",
		"google.com",
		"2001:db8::1",
		"2001:db8::/32",
		"::1",
		"0.0.0.0/0",
		"0.0.0.0/1",
		"10.0.0.0/7",
		"1.2.3.4/33",
		"1.2.3.4/-1",
		"0.1.2.3",
		"127.0.0.1",
		"169.254.1.1",
		"224.0.0.1",
		"240.0.0.1",
	}
	for _, bad := range invalid {
		if got, err := ParseIPv4CIDR(bad); err == nil {
			t.Fatalf("ParseIPv4CIDR(%q) expected error, got %q", bad, got)
		}
	}

	// 3. 去重与包含收敛（A 包含 B → 只保留 A）
	kept, skipped, err := NormalizeIPv4CIDRs([]string{
		"142.250.4.188",
		"142.250.4.99/24",
		"142.250.0.0/15",
		"142.250.0.0/15",
		"203.0.113.9/24",
		"203.0.113.5",
	})
	if err != nil {
		t.Fatalf("NormalizeIPv4CIDRs unexpected error: %v", err)
	}
	wantKept := []string{"142.250.0.0/15", "203.0.113.0/24"}
	if !reflect.DeepEqual(kept, wantKept) {
		t.Fatalf("kept = %v, want %v", kept, wantKept)
	}
	if len(skipped) != 4 {
		t.Fatalf("expected 4 skipped entries, got %v", skipped)
	}

	// 4. EffectiveProxyIPs 合并预设 ProxyIPs + 自定义 CustomIPs 并收敛
	rules := &ProxyDomainRules{
		ProxyIPs:  []string{"142.250.0.0/15", "172.253.0.0/16"},
		CustomIPs: []string{"142.250.4.188", "203.0.113.1/24"},
	}
	wantEff := []string{"142.250.0.0/15", "172.253.0.0/16", "203.0.113.0/24"}
	if got := rules.EffectiveProxyIPs(); !reflect.DeepEqual(got, wantEff) {
		t.Fatalf("EffectiveProxyIPs = %v, want %v", got, wantEff)
	}
}
