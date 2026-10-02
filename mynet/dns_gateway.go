package mynet

// 网关 DNS 接管（D3）。
//
// 产品裁定：
//   - 网关/Linux 一定接管 DNS（用户可显式关）
//   - 本机有智能 DNS（smartdns/dnsmasq）→ 用之，上游填我们的表
//   - 没有 → 接平台智能 DNS（每租户）
//
// 与 PC 侧（D4，改系统 DNS）不同：网关走 nft 劫持 + 本地解析器。

import (
	"fmt"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

// LocalResolver 探测到的本地解析器。
type LocalResolver struct {
	Kind string // smartdns | dnsmasq | none
	// HasLocal 是否有本机解析器可挂规则
	HasLocal bool
}

// DetectLocalResolver 从 system.json 能力里认解析器。
// smartdns 优先（能竞速/智能拆分），其次 dnsmasq，否则 none。
func DetectLocalResolver(snap *shared.SystemSnapshot) LocalResolver {
	if snap == nil {
		return LocalResolver{Kind: "none"}
	}
	if snap.Capabilities["smartdns"] {
		return LocalResolver{Kind: "smartdns", HasLocal: true}
	}
	if snap.Capabilities["dnsmasq"] {
		return LocalResolver{Kind: "dnsmasq", HasLocal: true}
	}
	return LocalResolver{Kind: "none"}
}

// GatewayDNSPlan 网关 DNS 接管方案（D3）。
type GatewayDNSPlan struct {
	// Resolver 用哪个本地解析器
	Resolver LocalResolver
	// TakeOver 是否接管（默认 true；网关强制）
	TakeOver bool
	// UpstreamDomestic 国内上游（dns-domestic / 默认公共）
	UpstreamDomestic []string
	// UpstreamClean 干净上游（dns-clean）
	UpstreamClean []string
	// PlatformDNS 租户智能 DNS（无本地解析器时的主上游）
	PlatformDNS string
	// ConfPath 本地解析器配置片段路径
	ConfPath string
}

// PlanGatewayDNS 网关 DNS 接管方案。
// rules 提供域名规则与上游；resolver 来自 DetectLocalResolver。
func PlanGatewayDNS(snap *shared.SystemSnapshot, rules *domain.ProxyDomainRules) GatewayDNSPlan {
	if rules == nil {
		rules = &domain.ProxyDomainRules{}
	}
	res := DetectLocalResolver(snap)
	plan := GatewayDNSPlan{
		Resolver:    res,
		TakeOver:    true, // 网关默认接管
		PlatformDNS: rules.EffectivePlatformDNS(),
		ConfPath:    "/tmp/mynet-dns-split.conf",
	}

	// 上游表：DomesticServers 有则用；否则公共国内兜底
	plan.UpstreamDomestic = rules.EffectiveDirectDNS()
	if len(plan.UpstreamDomestic) == 0 {
		plan.UpstreamDomestic = domain.DefaultDomesticDNS
	}
	// clean：TunnelServer > ExitDNS > 干净公共（由调用方填）
	plan.UpstreamClean = []string{}
	if rules.TunnelServer != "" {
		plan.UpstreamClean = append(plan.UpstreamClean, rules.TunnelServer)
	}
	if rules.ExitDNS != "" {
		plan.UpstreamClean = append(plan.UpstreamClean, rules.ExitDNS)
	}

	// 有本地解析器 → 用之（上游填我们的表）
	// 没有 → 接平台智能 DNS（主上游 = 租户 DNS）
	if !res.HasLocal {
		plan.ConfPath = "" // 不生成 dnsmasq 片段；由 DNSFormPC 路径处理
	}
	return plan
}

// DescribeGatewayDNS 一行诊断。
func DescribeGatewayDNS(p GatewayDNSPlan) string {
	if !p.TakeOver {
		return "dns: 不接管（用户关闭）"
	}
	if p.Resolver.HasLocal {
		return fmt.Sprintf("dns: 接管 → 本地 %s（domestic=%v clean=%v platform=%s）",
			p.Resolver.Kind, p.UpstreamDomestic, p.UpstreamClean, p.PlatformDNS)
	}
	return fmt.Sprintf("dns: 接管 → 平台智能 DNS %s（无本地解析器）", p.PlatformDNS)
}
