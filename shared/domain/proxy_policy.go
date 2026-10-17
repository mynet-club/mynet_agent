package domain

import "fmt"

// ProxyPolicy 代理分流意图（主线⑦「通过部分节点访问到不了的地方」）。
//
// 这是**节点级**业务意图，不是 Connection 级路径选择（那在 ConnectionPlan.TransportPolicy）。
// 后端按 Region×Policy 矩阵解算好 ListSource/MatchMode 后随 envelope 下发；
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
	// Split 由后端解算后的分流参数（ListSource + MatchMode + IP 列表引用）。
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

// 节点角色（ProxyPolicy.Role）。显式配置，不由是否存在 group 推断。
const (
	ProxyRoleClient = "client" // 仅按地区策略选择出口
	ProxyRoleServer = "server" // 仅作为出口节点放行（NAT 出网）
	ProxyRoleBoth   = "both"   // 同时具备两种能力
)

// ProxyPermission 云端下发的**出口许可**——wire 上 `network.proxy` 的唯一语义（T11）。
//
// 只回答「本节点被授权作为出口吗、对谁开放」。客户端分流策略（role/region/policy/
// split/exits/dns/domain_rules/doh_mode/full_tunnel/foreign_dns_route/fwmark/table）
// 高度依赖本机运行环境（系统 DNS / fwmark / ipset / dnsmasq / 网关形态），不走 wire，
// 由节点 Local 轨承载（见 proxy_intent.go）。两条轨道在 L6 落地时合成（proxy_unify.go）。
type ProxyPermission struct {
	// Enabled 本节点是否被授权作出口。false 时 L6 不装 server 侧 NAT/ACL。
	Enabled bool `json:"enabled"`
	// ACL 出口授权：对谁开放（对端级，T11）。空 = open（组织内）。
	ACL *ProxyACL `json:"acl,omitempty"`
}

// Validate 形状校验。
func (p *ProxyPermission) Validate() error {
	if p == nil {
		return nil
	}
	return p.ACL.Validate()
}

// AsCloudPolicy 把许可投影成 Cloud 轨可存的 ProxyPolicy（只带 Enabled + server 角色 + ACL）。
// 角色固定 server：许可就是「作为出口」的授权，不含 client 语义，这样 ProxyPolicy.Validate
// 不会把它当 client 策略要求 region/policy。
func (p *ProxyPermission) AsCloudPolicy() ProxyPolicy {
	if p == nil {
		return ProxyPolicy{}
	}
	return ProxyPolicy{Enabled: p.Enabled, Role: ProxyRoleServer, ACL: p.ACL}
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

// ProxyDNS DNS 策略（2026-10-01 修订：用户三选一，spec/proxy-split.md §13）。
//
// 核心洞察：指**公网**智能 DNS（UDP:53）可被链路劫持/污染；
// 唯一安全路径是把系统 DNS 指到 **peer 隧道 VIP**，查询走 gnb 加密封装。
// 不需要本地 forwarder —— 零常驻、零体积。
type ProxyDNS struct {
	// Mode 用户三选一：
	//   off         不加速（默认）—— 不碰系统 DNS
	//   peer        隧道节点 DNS —— 系统 DNS → peer VIP（走加密隧道，安全）
	//   smart       智能 DNS —— 系统 DNS → 智能 DNS（兜底，可能被劫持但不断网）
	//   peer_smart  叠加：peer 优先，smart 兜底（nameserver 顺序 fallback）
	Mode string `json:"mode,omitempty"`

	// PeerServers 隧道节点 DNS（mode=peer|peer_smart）。
	// Addr 是 peer 隧道 VIP（如 10.182.236.180），查询自动进 gnb 数据面。
	PeerServers []DNSServer `json:"peer_servers,omitempty"`

	// SmartServers 智能 DNS（mode=smart|peer_smart）。
	// Addr 是智能 DNS 地址（IP 或域名），兜底用。
	SmartServers []DNSServer `json:"smart_servers,omitempty"`

	// Hijack 强劫持范围：off | local | lan。**仅网关形态有意义**。
	// client（Win/Mac）不劫持 —— 只改系统 DNS 配置。
	// 网关也**不需要劫持** —— LAN 客户端本来就指网关当 DNS，只配上游即可。
	Hijack string `json:"hijack,omitempty"`

	// ── 旧字段（gateway 形态兼容，新码优先用上面的三选一）──
	// Mode 的旧值 none|redirect|resolv|split 仍被 gateway 侧解析逻辑读。
	// Server 国外 DNS（redirect/resolv 目标上游，split 的国外 DNS）。
	Server string `json:"server,omitempty"`
	// DomesticServers split 模式国内上游。
	DomesticServers []string `json:"domestic_servers,omitempty"`
	// RedirectTarget DNAT 目标（通常是出口 peer VIP）。mode=redirect 必填。
	RedirectTarget string `json:"redirect_target,omitempty"`
}

// DNSServer 一个 DNS 上游条目（三选一的来源，spec §13.4）。
// 与 ProxyExit 同形状：来源可租户下发 / 用户自建 / 朋友分享。
type DNSServer struct {
	// Source tenant | self | friend | platform —— 来源类型。
	Source string `json:"source,omitempty"`
	// Label 显示名（如「朋友的节点」「我的 dnsmasq」）。
	Label string `json:"label,omitempty"`
	// Addr 上游地址。peer=隧道 VIP；smart=IP 或域名。
	Addr string `json:"addr"`
	// NodeID peer 来源时的节点 ID（便于回链）。
	NodeID string `json:"node_id,omitempty"`
}

// DNS 模式常量（spec §13.1）。
const (
	DNSModeOff       = "off"        // 不加速（默认）
	DNSModePeer      = "peer"       // 隧道节点 DNS
	DNSModeSmart     = "smart"      // 智能 DNS
	DNSModePeerSmart = "peer_smart" // 叠加：peer 优先，smart 兜底
)

// Nameservers 按三选一模式推出 nameserver 列表（有序）。
// 顺序即优先级：peer 在前、smart 在后，OS/dnsmasq 自带 fallback。
//
//	off        → nil（不碰 DNS）
//	peer       → [peer VIP...]
//	smart      → [smart IP...]
//	peer_smart → [peer VIP..., smart IP...]
//
// 旧模式（none|redirect|resolv|split）返回 nil —— 由 gateway 侧旧逻辑处理。
func (d *ProxyDNS) Nameservers() []string {
	if d == nil {
		return nil
	}
	switch d.Mode {
	case DNSModePeer, DNSModePeerSmart:
		var out []string
		for _, s := range d.PeerServers {
			if s.Addr != "" {
				out = append(out, s.Addr)
			}
		}
		if d.Mode == DNSModePeerSmart {
			for _, s := range d.SmartServers {
				if s.Addr != "" {
					out = append(out, s.Addr)
				}
			}
		}
		return out
	case DNSModeSmart:
		var out []string
		for _, s := range d.SmartServers {
			if s.Addr != "" {
				out = append(out, s.Addr)
			}
		}
		return out
	default:
		return nil
	}
}

// Active 三选一是否开启了 DNS 处理（off/空 = 不碰）。
func (d *ProxyDNS) Active() bool {
	if d == nil {
		return false
	}
	switch d.Mode {
	case DNSModePeer, DNSModeSmart, DNSModePeerSmart:
		return len(d.Nameservers()) > 0
	default:
		return false
	}
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

// NormalizeSplitFields 把 region/policy/role 钳到合法枚举（非法时回退默认）。
// UI 侧 segify 按钮组在异常时序下可能串值（实测 region=dns policy=off/peer_smart），
// 后端不该因此硬拒保存 —— 钳到合法值继续，比报错卡死用户体验好（2026-10-17）。
func (p *ProxyPolicy) NormalizeSplitFields() {
	if p == nil {
		return
	}
	if p.Region != "" && p.Region != ProxyRegionDomestic && p.Region != ProxyRegionIntl {
		p.Region = ProxyRegionDomestic
	}
	if p.Policy != "" && p.Policy != ProxyPolicyWhitelist && p.Policy != ProxyPolicyBlacklist {
		p.Policy = ProxyPolicyWhitelist
	}
	if p.Role != "" && p.Role != "client" && p.Role != "server" && p.Role != "both" {
		p.Role = "client"
	}
}

// Validate 形状校验。enabled 时要求 split/exits/dns 的必要字段。
func (p *ProxyPolicy) Validate() error {
	if p == nil {
		return nil
	}
	p.NormalizeSplitFields()
	if !p.Enabled {
		return nil
	}
	if p.Role != "" && p.Role != "client" && p.Role != "server" && p.Role != "both" {
		return &ProxySplitError{Region: p.Region, Policy: "role=" + p.Role}
	}
	if p.IsClientLike() {
		if p.Region == "" {
			p.Region = ProxyRegionDomestic
		}
		if p.Policy == "" {
			p.Policy = ProxyPolicyWhitelist
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
		// 旧模式（gateway 兼容）
		case "", "none", "redirect", "resolv", "split":
		// 新三选一（spec §13.1）：off / peer / smart / peer_smart
		case DNSModeOff, DNSModePeer, DNSModeSmart, DNSModePeerSmart:
		default:
			return fmt.Errorf("proxy dns.mode 非法: %q（合法 off|peer|smart|peer_smart 或 none|redirect|resolv|split）", p.DNS.Mode)
		}
		if p.DNS.Mode == "redirect" && p.DNS.RedirectTarget == "" {
			return fmt.Errorf("proxy dns redirect 缺 redirect_target")
		}
	}
	return nil
}
