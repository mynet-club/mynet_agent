package domain

// ProxyPolicy 代理分流意图（主线⑦「通过部分节点访问到不了的地方」）。
//
// 这是**节点级**业务意图，不是 Connection 级路径选择（那在 ConnectionPlan.TransportPolicy）。
// back 按 Region×Policy 矩阵解算好 ListSource/MatchMode 后随 envelope 下发；
// 节点只落地，不在现算「谁走谁」（control-plane.md §7.2）。
//
// gnb 侧：`proxy on` + 出口对端（数据目标，等同 route.conf 下一跳；不写 address.conf）；
// wg 侧：AllowedIPs/hub 语义不同构，由 L5 各自解释（driver-abstraction.md §3）。
// OS 策略路由 / DNS / IP 集合归 L6（ApplyPlan），不含在本结构的驱动语法里。
type ProxyPolicy struct {
	// Enabled 总开关。false 时 L6 必须清理既有策略路由与 DNS 改动。
	Enabled bool `json:"enabled"`
	// LocalOnly 仅本机生效，不回写云端。适合「这台机只做本机 proxy」的场景。
	LocalOnly bool `json:"local_only,omitempty"`
	// Role client | server | both。server/both 侧负责 NAT 出网。
	Role string `json:"role,omitempty"`
	// Region domestic | international。与 Policy 共同决定 IP 集合与匹配极性。
	Region string `json:"region,omitempty"`
	// Policy whitelist | blacklist（业务输入，用户语义）。
	Policy string `json:"policy,omitempty"`
	// Split 由 back 解算后的分流参数（ListSource + MatchMode + IP 列表引用）。
	Split *ProxySplit `json:"split,omitempty"`
	// Exits 出站节点（有序；gnb 标 f）。
	Exits []ProxyExit `json:"exits,omitempty"`
	// DNS 策略。
	DNS *ProxyDNS `json:"dns,omitempty"`
	// DomainRules 域名分流规则（T13/T15）。与 split 正交：split 管 IP 段，这里管域名。
	DomainRules *ProxyDomainRules `json:"domain_rules,omitempty"`
	// ACL 出口授权：对谁开放（T11）。
	ACL *ProxyACL `json:"acl,omitempty"`
	// ForeignDNSRoute 无条件把公共国外 DNS /32 钉进隧道（旧线 route_foreign_dns）。
	// 仅 client/both 有意义。
	ForeignDNSRoute bool `json:"foreign_dns_route,omitempty"`
	// FullTunnel 全量进隧道（无域名规则时的全局模式）。
	FullTunnel bool `json:"full_tunnel,omitempty"`
	// DoHMode allow|block|tunnel — 加密 DNS 旁路处理（T16）。
	DoHMode string `json:"doh_mode,omitempty"`
	// Fwmark 策略路由标记（旧线 0xc8）。0 时 L6 用默认 200。
	Fwmark int `json:"fwmark,omitempty"`
	// Table 策略路由表号（旧线 200）。0 时 L6 用默认 200。
	Table int `json:"table,omitempty"`
}

// ProxySplit 地域分流参数。ListSource/MatchMode 由 ResolveProxySplit 解算，
// **调用方不得自行拼**——旧线曾因「只按 region 选数据源」导致 domestic+blacklist 失效。
type ProxySplit struct {
	// ListSource china | international —— 节点要加载的 IP 集合。
	ListSource string `json:"list_source"`
	// MatchMode normal | inverted。
	// normal：命中集合 → 走代理；inverted：未命中集合 → 走代理。
	MatchMode string `json:"match_mode"`
	// IPList 列表获取方式（URL + 校验）。
	IPList *ProxyIPListRef `json:"ip_list,omitempty"`
}

// ProxyIPListRef IP 段列表引用。节点按 URL 拉取并校验 sha256，不在 envelope 里内嵌全量 CIDR。
type ProxyIPListRef struct {
	// Source china | international（与 Split.ListSource 一致，冗余便于审计）。
	Source string `json:"source"`
	// URL 下载地址（ctl 的 proxy-lists 发布点）。
	URL string `json:"url"`
	// SHA256 十六进制小写。空表示「下载后不校验」——不推荐，仅过渡用。
	SHA256 string `json:"sha256,omitempty"`
	// Bytes 期望字节数（可选，提前发现截断）。
	Bytes int64 `json:"bytes,omitempty"`
}

// ProxyDNS DNS 策略（旧线 DNS_MODE + 新线强劫持）。
type ProxyDNS struct {
	// Mode none | redirect | resolv | split。
	Mode string `json:"mode,omitempty"`
	// Hijack 强劫持范围：off | local（仅本机）| lan（网关，含 LAN）。
	// 与 Mode 正交：hijack 负责「把 :53 收到本地解析器」，Mode 负责「本地解析器怎么分流」。
	Hijack string `json:"hijack,omitempty"`
	// Server 国外 DNS（redirect/resolv 目标上游，split 的国外 DNS）。
	Server string `json:"server,omitempty"`
	// DomesticServers split 模式国内上游。
	DomesticServers []string `json:"domestic_servers,omitempty"`
	// RedirectTarget DNAT 目标（通常是出口 peer VIP）。mode=redirect 必填。
	RedirectTarget string `json:"redirect_target,omitempty"`
}

// ── Region × Policy 矩阵 ─────────────────────────────────────
//
// CRITICAL（spec/proxy.md）：IP 列表数据源由 NODE_REGION **和** PROXY_POLICY 共同决定。
// 旧版只按 region 选源，domestic+blacklist 时加载 interip 再反转匹配，
// 结果境外 IP 不走代理——必须用 ResolveProxySplit，不要手工分支。

const (
	ProxyListChina         = "china"
	ProxyListInternational = "international"
	ProxyMatchNormal       = "normal"
	ProxyMatchInverted     = "inverted"
	ProxyPolicyWhitelist   = "whitelist"
	ProxyPolicyBlacklist   = "blacklist"
	ProxyRegionDomestic    = "domestic"
	ProxyRegionIntl        = "international"
)

// ResolveProxySplit 解算 Region×Policy → (ListSource, MatchMode)。
// region/policy 非法时返回 error，不静默兜底。
func ResolveProxySplit(region, policy string) (listSource, matchMode string, err error) {
	switch region {
	case ProxyRegionDomestic:
		switch policy {
		case ProxyPolicyWhitelist:
			// 命中海外 IP → 走代理（国内直连，海外加速）
			return ProxyListInternational, ProxyMatchNormal, nil
		case ProxyPolicyBlacklist:
			// 未命中国内 IP → 走代理（等效全局代理，国内直连）
			return ProxyListChina, ProxyMatchInverted, nil
		}
	case ProxyRegionIntl:
		switch policy {
		case ProxyPolicyWhitelist:
			// 命中国内 IP → 走代理（海外节点回国加速）
			return ProxyListChina, ProxyMatchNormal, nil
		case ProxyPolicyBlacklist:
			// 未命中海外 IP → 走代理
			return ProxyListInternational, ProxyMatchInverted, nil
		}
	}
	return "", "", &ProxySplitError{Region: region, Policy: policy}
}

// ProxySplitError 解析失败。
type ProxySplitError struct {
	Region string
	Policy string
}

func (e *ProxySplitError) Error() string {
	return "proxy split 参数非法: region=" + e.Region + " policy=" + e.Policy +
		"（合法 region=domestic|international, policy=whitelist|blacklist）"
}

// DefaultProxyFwmark / DefaultProxyTable 旧线默认值（spec/proxy.md）。
const (
	DefaultProxyFwmark = 0xc8
	DefaultProxyTable  = 200
)

// EffectiveFwmark 0 时取默认。
func (p *ProxyPolicy) EffectiveFwmark() int {
	if p == nil || p.Fwmark == 0 {
		return DefaultProxyFwmark
	}
	return p.Fwmark
}

// EffectiveTable 0 时取默认。
func (p *ProxyPolicy) EffectiveTable() int {
	if p == nil || p.Table == 0 {
		return DefaultProxyTable
	}
	return p.Table
}

// IsClientLike 是否要装 client 侧策略路由（fwmark/ipset/DNS）。
func (p *ProxyPolicy) IsClientLike() bool {
	if p == nil || !p.Enabled {
		return false
	}
	return p.Role == "client" || p.Role == "both" || p.Role == ""
}

// IsServerLike 是否要装 server 侧 NAT/转发。
func (p *ProxyPolicy) IsServerLike() bool {
	if p == nil || !p.Enabled {
		return false
	}
	return p.Role == "server" || p.Role == "both"
}

// Validate 形状校验。enabled 时要求 split/exits/dns 的必要字段。
func (p *ProxyPolicy) Validate() error {
	if p == nil {
		return nil
	}
	if !p.Enabled {
		return nil
	}
	if p.Role != "" && p.Role != "client" && p.Role != "server" && p.Role != "both" {
		return &ProxySplitError{Region: p.Region, Policy: "role=" + p.Role}
	}
	if p.IsClientLike() {
		if p.Region == "" || p.Policy == "" {
			return &ProxySplitError{Region: p.Region, Policy: p.Policy}
		}
		// 与解算结果交叉验证，防止上游手写错
		src, mode, err := ResolveProxySplit(p.Region, p.Policy)
		if err != nil {
			return err
		}
		if p.Split != nil {
			if p.Split.ListSource != src || p.Split.MatchMode != mode {
				return &ProxySplitError{
					Region: "split不一致:" + p.Split.ListSource + "/" + p.Split.MatchMode,
					Policy: "期望:" + src + "/" + mode,
				}
			}
		}
	}
	// 允许 enabled 暂无 exits（先开后补）
	if err := p.ACL.Validate(); err != nil {
		return err
	}
	if p.DNS != nil {
		switch p.DNS.Mode {
		case "", "none", "redirect", "resolv", "split":
		default:
			return &ProxySplitError{Region: "dns", Policy: p.DNS.Mode}
		}
		if p.DNS.Mode == "redirect" && p.DNS.RedirectTarget == "" {
			return &ProxySplitError{Region: "dns", Policy: "redirect 缺 redirect_target"}
		}
	}
	return nil
}
