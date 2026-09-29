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

// PlatformDNSDefault 平台智能 DNS（目标态）。过渡期可被配置覆盖为公共 DNS。
const PlatformDNSDefault = "dns.mynet.club"

// DefaultDomesticDNS 国内公共 DNS 兜底。
var DefaultDomesticDNS = []string{"223.5.5.5", "119.29.29.29"}

type ProxyDomainRules struct {
	// Proxied 走代理的域名（精确或 `.suffix` / `*.suffix`）。
	Proxied []string `json:"proxied,omitempty"`
	// Direct 直连域名。
	Direct []string `json:"direct,omitempty"`
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

func (r *ProxyDomainRules) EffectivePlatformDNS() string {
	if r == nil || r.PlatformDNS == "" {
		return PlatformDNSDefault
	}
	return r.PlatformDNS
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

// IsProxied 是否在 proxied 规则内。
func (r *ProxyDomainRules) IsProxied(domain string) bool {
	if r == nil {
		return false
	}
	d := normalizeDomain(domain)
	if matchDomainList(d, r.Direct) {
		return false
	}
	return matchDomainList(d, r.Proxied)
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
