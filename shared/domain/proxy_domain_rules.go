package domain

// ProxyDomainRules 域名分流规则（proxy-split.md §3.4–3.5）。
//
// DNS 与流量解耦：规则决定「域名的 DNS 上游」和「是否计入 proxied 集合」；
// 是否走隧道由 L6 按 ipset/fwmark 执行。
//
// 形态：
//   - 网关：规则 → dnsmasq `server=/domain/上游`
//   - PC：不装 dnsmasq，只指平台 DNS / 公共 DNS（可选轻劫持）

import (
	"strings"
)

// 上游策略。
const (
	DNSUpstreamFast     = "fast"     // 国内/平台 DNS，不进隧道（默认）
	DNSUpstreamClean    = "clean"    // 隧道/出口 DNS（仅 clean 列表）
	DNSUpstreamBalanced = "balanced" // 竞速 / 过滤污染（SmartDNS）
)

// PlatformDNSDefault 平台智能 DNS —— 外部/非大陆（D1，2026-09-30 用户定案）。
// 与后端的 DnsUpstreamResolver 同源：
//
//	dns.mynet.club  外部 DNS（支持 DoH）
//	dns.dsplat.com  大陆 DNS（支持 DoH）
//
// 租户未配置 → 继承平台默认；services 表可按租户覆盖。
const PlatformDNSDefault = "dns.mynet.club"

// PlatformDNSDomestic 大陆智能 DNS（大陆客户端 region=domestic）。
const PlatformDNSDomestic = "dns.dsplat.com"

// PlatformDNSForRegion 按客户端 region 选平台默认 DNS。
// region=domestic → dns.dsplat.com；其余 → dns.mynet.club。
func PlatformDNSForRegion(region string) string {
	if region == "domestic" {
		return PlatformDNSDomestic
	}
	return PlatformDNSDefault
}

// DefaultDomesticDNS 国内公共 DNS 兜底。
var DefaultDomesticDNS = []string{"223.5.5.5", "119.29.29.29"}

type ProxyDomainRules struct {
	// Proxied 走代理的域名（精确或 `.suffix` / `*.suffix`）。
	// 基础清单（ctl 下发 proxy-domains.txt）落这里。
	Proxied []string `json:"proxied,omitempty"`
	// Direct 直连域名（ctl 下发 direct-domains.txt 白名单）。
	Direct []string `json:"direct,omitempty"`
	// CustomDomains 用户本机追加的代理域名（D5）。叠加在 Proxied 之上，不回写云端。
	CustomDomains []string `json:"custom_domains,omitempty"`
	// CustomDirect 用户本机追加的直连域名。优先级最高。
	CustomDirect []string `json:"custom_direct,omitempty"`
	// CustomIPs 用户本机追加的代理 IP（PC 形态 /32 钉扎用，不经 DNS 解析）。
	// 与 CustomDomains 同级：本机叠加、不回写云端。仅 PC pin 消费；网关 nft 集合不读它。
	CustomIPs []string `json:"custom_ips,omitempty"`
	// UpstreamProxied fast|clean|balanced。空 = fast。
	UpstreamProxied string `json:"upstream_proxied,omitempty"`
	// UpstreamClean 仅这些域名的 DNS 走隧道/出口（clean）。
	UpstreamClean []string `json:"upstream_clean,omitempty"`
	// UpstreamDirect 国内上游。
	UpstreamDirect []string `json:"upstream_direct,omitempty"`
	// PlatformDNS 平台智能 DNS；空则用 PlatformDNSDefault（PC 形态主上游）。
	PlatformDNS string `json:"platform_dns,omitempty"`
	// TunnelServer 隧道内国际 DNS（clean 用）。
	TunnelServer string `json:"tunnel_server,omitempty"`
	// ExitDNS 出口节点 DNS（clean 可选第二档）。
	ExitDNS string `json:"exit_dns,omitempty"`
}

// MergePriority 合并优先级（D5，钉死）：
//
//	自定义直连 > 直连白名单 > 自定义代理 > 基础清单 > 默认
//
// EffectiveProxied / EffectiveDirect 按此算出最终两组。
// 自定义不回写云端，只在本机叠加。

// EffectiveDirect 直连并集：CustomDirect ∪ Direct。
func (r *ProxyDomainRules) EffectiveDirect() []string {
	if r == nil {
		return nil
	}
	return unionDomains(r.CustomDirect, r.Direct)
}

// EffectiveProxied 代理并集：(Proxied ∪ CustomDomains) − EffectiveDirect。
// 直连优先：命中的从代理组剔除。
func (r *ProxyDomainRules) EffectiveProxied() []string {
	if r == nil {
		return nil
	}
	all := unionDomains(r.Proxied, r.CustomDomains)
	direct := r.EffectiveDirect()
	if len(direct) == 0 {
		return all
	}
	var out []string
	for _, d := range all {
		if !matchDomainList(normalizeDomain(d), direct) {
			out = append(out, d)
		}
	}
	return out
}

func unionDomains(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, raw := range list {
			d := normalizeDomain(raw)
			if d == "" || seen[d] {
				continue
			}
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

func (r *ProxyDomainRules) EffectivePlatformDNS() string {
	if r == nil || r.PlatformDNS == "" {
		return PlatformDNSDefault
	}
	return r.PlatformDNS
}

// ApplyTenantDNS D1：把租户智能 DNS 塞进 PlatformDNS（未显式配置时）。
// 解析优先级：显式 PlatformDNS > profile.TenantDNS / <tenant>.dns.mynet.club > 默认。
func (r *ProxyDomainRules) ApplyTenantDNS(tenantDNS string) {
	if r == nil {
		return
	}
	if r.PlatformDNS == "" && tenantDNS != "" {
		r.PlatformDNS = tenantDNS
	}
}

func (r *ProxyDomainRules) EffectiveUpstreamProxied() string {
	if r == nil || r.UpstreamProxied == "" {
		return DNSUpstreamFast
	}
	return r.UpstreamProxied
}

func (r *ProxyDomainRules) EffectiveDirectDNS() []string {
	if r == nil || len(r.UpstreamDirect) == 0 {
		return DefaultDomesticDNS
	}
	return r.UpstreamDirect
}

// IsClean 域名是否需要干净解析（DNS 进隧道）。
func (r *ProxyDomainRules) IsClean(domain string) bool {
	if r == nil {
		return false
	}
	d := normalizeDomain(domain)
	return matchDomainList(d, r.UpstreamClean)
}

// IsProxied 是否在 proxied 规则内（含自定义叠加，D5 优先级）。
func (r *ProxyDomainRules) IsProxied(domain string) bool {
	if r == nil {
		return false
	}
	d := normalizeDomain(domain)
	// 优先级：自定义直连 > 白名单 > 自定义代理 > 基础清单
	if matchDomainList(d, r.EffectiveDirect()) {
		return false
	}
	return matchDomainList(d, r.EffectiveProxied())
}

// IsDirect 是否强制直连（CustomDirect ∪ Direct）。
func (r *ProxyDomainRules) IsDirect(domain string) bool {
	if r == nil {
		return false
	}
	return matchDomainList(normalizeDomain(domain), r.EffectiveDirect())
}

func normalizeDomain(d string) string {
	d = strings.TrimSpace(strings.ToLower(d))
	d = strings.TrimPrefix(d, "*.")
	d = strings.TrimPrefix(d, ".")
	return d
}

// matchDomainList 精确或后缀匹配。支持 `example.com` / `.example.com` / `*.example.com`。
func matchDomainList(domain string, list []string) bool {
	for _, raw := range list {
		item := normalizeDomain(raw)
		if item == "" {
			continue
		}
		if domain == item || strings.HasSuffix(domain, "."+item) {
			return true
		}
	}
	return false
}

// ResolveDNSSplit 域名 → 上游 IP 列表（供 dnsmasq / tiny forwarder 用）。
//
// 优先级：clean 列表 > proxied 策略 > direct。
func (r *ProxyDomainRules) ResolveDNSSplit(domain string) (upstream []string, via string) {
	d := normalizeDomain(domain)
	if r.IsClean(d) {
		if r.TunnelServer != "" {
			return []string{r.TunnelServer}, DNSUpstreamClean
		}
		if r.ExitDNS != "" {
			return []string{r.ExitDNS}, DNSUpstreamClean
		}
		// clean 要求干净但未配隧道 DNS → 仍走 clean 语义，上游用平台 DNS
		return []string{r.EffectivePlatformDNS()}, DNSUpstreamClean
	}
	if matchDomainList(d, r.Direct) {
		return r.EffectiveDirectDNS(), "direct"
	}
	switch r.EffectiveUpstreamProxied() {
	case DNSUpstreamClean:
		if r.TunnelServer != "" {
			return []string{r.TunnelServer}, DNSUpstreamClean
		}
		return []string{r.EffectivePlatformDNS()}, DNSUpstreamClean
	case DNSUpstreamBalanced:
		// 调用方（SmartDNS）竞速；这里给出候选
		out := append([]string{}, r.EffectiveDirectDNS()...)
		out = append(out, r.EffectivePlatformDNS())
		return out, DNSUpstreamBalanced
	default: // fast
		if r.IsProxied(d) {
			// proxied 但 fast：平台 DNS（快，尽量准），不进隧道
			return []string{r.EffectivePlatformDNS()}, DNSUpstreamFast
		}
		return r.EffectiveDirectDNS(), "direct"
	}
}

// ── T15：规则来源合并（本地 + 云端，后覆盖先）────────────────

// MergeDomainRules 合并两份域名规则。
//
// 原则（proxy-split.md）：沿用双控「后覆盖先」——以 later 为基，
// earlier 仅在 later 缺省的字段上补齐；列表类做并集且 later 同项去重优先。
//
// 不改指针入参，返回新对象。
func MergeDomainRules(earlier, later *ProxyDomainRules) *ProxyDomainRules {
	if earlier == nil && later == nil {
		return nil
	}
	if later == nil {
		return cloneDomainRules(earlier)
	}
	if earlier == nil {
		return cloneDomainRules(later)
	}
	out := cloneDomainRules(later)
	// 标量：later 非空即胜；空则继承 earlier
	if out.UpstreamProxied == "" {
		out.UpstreamProxied = earlier.UpstreamProxied
	}
	if out.PlatformDNS == "" {
		out.PlatformDNS = earlier.PlatformDNS
	}
	if out.TunnelServer == "" {
		out.TunnelServer = earlier.TunnelServer
	}
	if out.ExitDNS == "" {
		out.ExitDNS = earlier.ExitDNS
	}
	// 列表：并集，later 在前（同项去重）
	out.Proxied = unionStrings(later.Proxied, earlier.Proxied)
	out.Direct = unionStrings(later.Direct, earlier.Direct)
	out.UpstreamClean = unionStrings(later.UpstreamClean, earlier.UpstreamClean)
	out.CustomIPs = unionStrings(later.CustomIPs, earlier.CustomIPs)
	if len(out.UpstreamDirect) == 0 {
		out.UpstreamDirect = append([]string{}, earlier.UpstreamDirect...)
	}
	return out
}

func cloneDomainRules(r *ProxyDomainRules) *ProxyDomainRules {
	if r == nil {
		return nil
	}
	c := *r
	c.Proxied = append([]string{}, r.Proxied...)
	c.Direct = append([]string{}, r.Direct...)
	c.UpstreamClean = append([]string{}, r.UpstreamClean...)
	c.UpstreamDirect = append([]string{}, r.UpstreamDirect...)
	c.CustomIPs = append([]string{}, r.CustomIPs...)
	return &c
}

func unionStrings(first []string, second []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, group := range [][]string{first, second} {
		for _, s := range group {
			s = strings.TrimSpace(s)
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// EffectiveDomainRules 取合并后的域名规则（云端与本地，后覆盖先）。
func (s *ProxyIntentState) EffectiveDomainRules() *ProxyDomainRules {
	if s == nil {
		return nil
	}
	var local, cloud *ProxyDomainRules
	if s.Local != nil {
		local = s.Local.Policy.DomainRules
	}
	if s.Cloud != nil {
		cloud = s.Cloud.Policy.DomainRules
	}
	// 后覆盖先：与 Effective() 同源——谁的 policy 生效，以它的 rules 为 later
	eff := s.Effective()
	if eff != nil && eff.Source == ProxySourceLocal {
		return MergeDomainRules(cloud, local)
	}
	if eff != nil {
		return MergeDomainRules(local, cloud)
	}
	return MergeDomainRules(local, cloud)
}
