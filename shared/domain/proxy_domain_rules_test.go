package domain

import (
	"strconv"
	"testing"
)

// D5：清单 + 白名单 + 自定义叠加的合并优先级。
//
//	自定义直连 > 白名单 > 自定义代理 > 基础清单
func TestEffectiveDomainsPriority(t *testing.T) {
	r := &ProxyDomainRules{
		Proxied:       []string{"google.com", "github.com", "example.com"},
		Direct:        []string{"qq.com", "github.com"}, // 白名单压掉基础清单里的 github
		CustomDomains: []string{"abc.com", "google.com"}, // 自定义代理
		CustomDirect:  []string{"google.com"},            // 自定义直连压掉一切
	}

	// 最终代理：github 被白名单剔除，google 被自定义直连剔除
	effP := r.EffectiveProxied()
	wantP := map[string]bool{"example.com": true, "abc.com": true}
	if len(effP) != 2 {
		t.Fatalf("EffectiveProxied=%v want 2 条", effP)
	}
	for _, d := range effP {
		if !wantP[d] {
			t.Fatalf("不应在代理组: %s（%v）", d, effP)
		}
	}

	// 最终直连：custom ∪ 白名单
	effD := r.EffectiveDirect()
	wantD := map[string]bool{"google.com": true, "qq.com": true, "github.com": true}
	if len(effD) != 3 {
		t.Fatalf("EffectiveDirect=%v want 3 条", effD)
	}
	for _, d := range effD {
		if !wantD[d] {
			t.Fatalf("不应在直连组: %s（%v）", d, effD)
		}
	}

	// IsProxied / IsDirect
	if r.IsProxied("google.com") {
		t.Fatal("google.com 被自定义直连，不该走代理")
	}
	if !r.IsDirect("google.com") {
		t.Fatal("google.com 应直连")
	}
	if r.IsProxied("github.com") {
		t.Fatal("github.com 在白名单，不该走代理")
	}
	if !r.IsProxied("abc.com") {
		t.Fatal("abc.com 自定义代理，应走代理")
	}
	if !r.IsProxied("example.com") {
		t.Fatal("example.com 基础清单，应走代理")
	}
}

// 100 条清单 + 加 abc.com = 101 条生效（用户场景）。
func TestCustomAppendsToBaseList(t *testing.T) {
	base := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		base = append(base, "site"+strconv.Itoa(i)+".com")
	}
	r := &ProxyDomainRules{
		Proxied:       base,
		CustomDomains: []string{"abc.com"},
	}
	eff := r.EffectiveProxied()
	if len(eff) != 101 {
		t.Fatalf("合并后 %d 条，want 101", len(eff))
	}
	found := false
	for _, d := range eff {
		if d == "abc.com" {
			found = true
		}
	}
	if !found {
		t.Fatal("abc.com 应在合并结果里")
	}
}

func TestNormalizeAndMatch(t *testing.T) {
	r := &ProxyDomainRules{CustomDomains: []string{"*.Example.COM", ".foo.org"}}
	if !r.IsProxied("www.example.com") {
		t.Fatal("*.Example.COM 应匹配 www.example.com")
	}
	if !r.IsProxied("foo.org") {
		t.Fatal(".foo.org 应匹配 foo.org")
	}
}

// D1：租户智能 DNS 优先级。
func TestApplyTenantDNS(t *testing.T) {
	// 显式 PlatformDNS 不被覆盖
	r := &ProxyDomainRules{PlatformDNS: "dns.example.com"}
	r.ApplyTenantDNS("t1.dns.mynet.club")
	if r.EffectivePlatformDNS() != "dns.example.com" {
		t.Fatalf("显式配置不该被覆盖: %s", r.EffectivePlatformDNS())
	}

	// 未配置 → 租户 DNS
	r2 := &ProxyDomainRules{}
	r2.ApplyTenantDNS("t1.dns.mynet.club")
	if r2.EffectivePlatformDNS() != "t1.dns.mynet.club" {
		t.Fatalf("应吃租户 DNS: %s", r2.EffectivePlatformDNS())
	}

	// 空 → 默认
	r3 := &ProxyDomainRules{}
	if r3.EffectivePlatformDNS() != PlatformDNSDefault {
		t.Fatalf("默认 %s", r3.EffectivePlatformDNS())
	}

	// profile：未填 TenantDNS → 全局默认（不再自动生成 <tenant>.dns.mynet.club）
	p := &OnlineProfile{TenantID: "3678250676883169"}
	if p.EffectiveTenantDNS() != PlatformDNSDefault {
		t.Fatalf("未填 TenantDNS 应用默认: %s", p.EffectiveTenantDNS())
	}
	p2 := &OnlineProfile{TenantID: "t1", TenantDNS: "custom.dns"}
	if p2.EffectiveTenantDNS() != "custom.dns" {
		t.Fatalf("显式优先: %s", p2.EffectiveTenantDNS())
	}
}
