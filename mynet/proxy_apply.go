package mynet

// L6 代理分流落地（主线⑦）。
//
// 输入是 bundle.network.proxy（domain.ProxyPolicy，back 已解算好 Region×Policy），
// 输出是系统命令计划。驱动无关：gnb0 / wg0 一视同仁。
//
// 旧线（spec/proxy.md）的完整语义保留在此，但**不再往 route.conf 灌海量 /8**——
// 未登记目的地兜底由 gnb `proxy on` 负责；这里只做 OS 策略路由 + IP 集合 + DNS。

import (
	"fmt"
	"strings"

	"github.com/mynet-club/mynet_agent-V2/shared"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

// DefaultForeignDNSServers 公共国外 DNS（spec/proxy.md 的 SSOT 列表）。
// 无条件钉 /32 进隧道，防 ISP 污染。
var DefaultForeignDNSServers = []string{
	"8.8.8.8", "8.8.4.4",
	"1.1.1.1", "1.0.0.1",
	"9.9.9.9",
	"208.67.222.222", "208.67.220.220",
}

// ProxyRuntime 代理落地所需的运行时上下文（接口名等）。
// Device 用逻辑名，执行期由 resolveDevice 纠正（utunN / P2PNet）。
//
// 同机（消费者=入口）必须防自环（proxy-split.md §1.0）：
// 隧道自身 UDP / 出口物理 endpoint 绝不能被 mark 进 table。
type ProxyRuntime struct {
	TunDevice string // 逻辑隧道口，如 gnb_tun
	ExitVia   string // 出口 peer VIP，策略路由 default via
	// ExitEndpoint 出口对端的**物理** endpoint IP（可选）。
	// 非空时钉物理口，防止被策略路由卷回隧道。
	ExitEndpoint string
	// TunnelLocalPort 本机隧道 UDP 监听端口（gnb）。豁免该源端口出站。
	TunnelLocalPort int
	LANBridge       string // 可选，LAN 透明接入时的桥（br-lan）
	WANDevice       string // 可选，物理出口（网关 /32 修复）
	WANGateway      string // 可选，WAN 网关 IP
	IPSetFile       string // CIDR 列表文件路径（chinaip/interip）
}

// proxyPrio 策略路由优先级（旧线 31800）。
const proxyPrio = 31800

// ProxyApplyPlan 代理分流写入计划（挂进 ApplyPlan.Proxy）。
type ProxyApplyPlan struct {
	Policy  *domain.ProxyPolicy `json:"policy,omitempty"`
	Runtime ProxyRuntime        `json:"runtime"`
}

// planProxy 从 NetworkIntent.Proxy + 运行时上下文生成计划。
func planProxy(p *domain.ProxyPolicy, rt ProxyRuntime) *ProxyApplyPlan {
	if p == nil || !p.Enabled {
		return nil
	}
	return &ProxyApplyPlan{Policy: p, Runtime: rt}
}

// proxyClientCmds client/both 侧：IP 集合 + 策略路由 + mangle 打标 + WAN 修复。
// 纯函数，便于断言命令形状（D2：不测 fixture skip，测真生成结果）。
func proxyClientCmds(snap *shared.SystemSnapshot, p *domain.ProxyPolicy, rt ProxyRuntime) []routeCmd {
	if !p.IsClientLike() {
		return nil
	}
	fw := p.EffectiveFwmark()
	table := p.EffectiveTable()
	dev := rt.TunDevice
	var cmds []routeCmd

	// 1) IP 集合：nft 优先，ipset 兜底
	cmds = append(cmds, proxyIPSetCmds(snap, p, rt, fw)...)

	// 1.5) 同机自环豁免（T17）：必须在打标之前
	cmds = append(cmds, proxySelfExemptCmds(snap, rt)...)

	// 2) 策略路由表：default via exit dev tun table N
	if rt.ExitVia != "" {
		cmds = append(cmds, routeCmd{argv: []string{
			"ip", "route", "replace", "default", "via", rt.ExitVia, "dev", dev, "table", fmt.Sprintf("%d", table),
		}})
	} else {
		// 无 VIP 时钉设备（对端用 on-link）
		cmds = append(cmds, routeCmd{argv: []string{
			"ip", "route", "replace", "default", "dev", dev, "table", fmt.Sprintf("%d", table),
		}})
	}

	// 3) ip rule: fwmark → table
	// 先删后加，幂等；del 失败可忽略
	cmds = append(cmds,
		routeCmd{argv: []string{
			"ip", "rule", "del", "fwmark", fmt.Sprintf("%#x", fw), "lookup", fmt.Sprintf("%d", table),
		}, optional: true},
		routeCmd{argv: []string{
			"ip", "rule", "add", "fwmark", fmt.Sprintf("%#x", fw), "lookup", fmt.Sprintf("%d", table), "prio", fmt.Sprintf("%d", proxyPrio),
		}},
	)

	// 4) WAN 网关 /32 修复：防隧道 /24 盖住物理直连
	if rt.WANGateway != "" && rt.WANDevice != "" {
		cmds = append(cmds, routeCmd{argv: []string{
			"ip", "route", "replace", rt.WANGateway + "/32", "dev", rt.WANDevice,
		}, optional: true})
	}

	// 5) mangle 打标
	cmds = append(cmds, proxyMarkCmds(snap, p, rt, fw)...)

	// 6) 国外 DNS /32 强制进隧道（写主表，无条件）
	if p.ForeignDNSRoute {
		for _, ip := range DefaultForeignDNSServers {
			cmds = append(cmds, routeCmd{argv: []string{
				"ip", "route", "replace", ip + "/32", "dev", dev,
			}})
		}
	}

	return cmds
}

// proxyIPSetCmds 创建/加载 IP 集合。
// MATCH_MODE=normal：命中 → mark；inverted：未命中 → mark（由 mark 规则体现）。
func proxyIPSetCmds(snap *shared.SystemSnapshot, p *domain.ProxyPolicy, rt ProxyRuntime, fw int) []routeCmd {
	if rt.IPSetFile == "" {
		return nil
	}
	if snap.Capabilities["nft"] {
		return []routeCmd{
			{argv: []string{"nft", "add", "table", "ip", "mynet_proxy"}},
			{argv: []string{"nft", "add", "set", "ip", "mynet_proxy", "dst4",
				"{ type ipv4_addr; flags interval; }"}, optional: true},
			// 从文件灌元素（nft -f 片段由执行器包装；此处用 nft add element 批量不现实，
			// 真正加载走 proxyLoadIPSetFile —— 命令仅占位确保 set 存在）。
			{argv: []string{"nft", "flush", "set", "ip", "mynet_proxy", "dst4"}, optional: true},
		}
	}
	if snap.Capabilities["iptables"] {
		return []routeCmd{
			{argv: []string{"ipset", "create", "mynet_proxy", "hash:net", "-exist"}},
			{argv: []string{"ipset", "flush", "mynet_proxy"}, optional: true},
		}
	}
	return nil
}

// proxyMarkCmds 打 fwmark：PREROUTING（LAN 转发）+ OUTPUT（本机 App，同机形态）。
//
// 历史教训：只写 PREROUTING 时，消费者=入口的 PC 上**本机流量根本不会被标记**。
// 同机自环见 proxySelfExemptCmds —— 隧道自身包必须先豁免再谈打标。
func proxyMarkCmds(snap *shared.SystemSnapshot, p *domain.ProxyPolicy, rt ProxyRuntime, fw int) []routeCmd {
	if p.Split == nil {
		return nil
	}
	inverted := p.Split.MatchMode == domain.ProxyMatchInverted
	fwHex := fmt.Sprintf("%#x", fw)

	if snap.Capabilities["nft"] {
		cmds := []routeCmd{
			{argv: []string{"nft", "add", "chain", "ip", "mangle", "PREROUTING",
				"{ type filter hook prerouting priority -150; }"}, optional: true},
			// 本机 App（消费者=入口）
			{argv: []string{"nft", "add", "chain", "ip", "mangle", "OUTPUT",
				"{ type filter hook output priority -150; }"}, optional: true},
		}
		// LAN 回包不重打 mark（防路由回环）
		if rt.LANBridge != "" {
			cmds = append(cmds, routeCmd{argv: []string{
				"nft", "add", "rule", "ip", "mangle", "PREROUTING",
				"iifname", rt.TunDevice, "ct", "state", "established,related", "return",
			}, optional: true})
		}
		mark := func(chain, daddrExpr string) []string {
			base := []string{"nft", "add", "rule", "ip", "mangle", chain}
			if inverted {
				return append(base, "ip", "daddr", "!=", daddrExpr, "meta", "mark", "set", fwHex)
			}
			return append(base, "ip", "daddr", daddrExpr, "meta", "mark", "set", fwHex)
		}
		setRef := "@mynet_proxy dst4"
		cmds = append(cmds, routeCmd{argv: mark("PREROUTING", setRef)})
		cmds = append(cmds, routeCmd{argv: mark("OUTPUT", setRef)})
		return cmds
	}

	if snap.Capabilities["iptables"] {
		setMatch := []string{"-m", "set", "--match-set", "mynet_proxy", "dst"}
		jump := []string{"-j", "MARK", "--set-mark", fwHex}
		var neg []string
		if inverted {
			neg = []string{"!"}
		}
		mark := func(chain string) []string {
			argv := []string{"iptables", "-t", "mangle", "-A", chain}
			argv = append(argv, neg...)
			argv = append(argv, setMatch...)
			return append(argv, jump...)
		}
		return []routeCmd{{argv: mark("PREROUTING")}, {argv: mark("OUTPUT")}}
	}
	return nil
}

// proxySelfExemptCmds 同机自环豁免（proxy-split.md §1.0 / T17）。
//
// 必须在打标/策略路由生效**前**装好，否则 gnb UDP 会被卷进 table 200 → 自环。
func proxySelfExemptCmds(snap *shared.SystemSnapshot, rt ProxyRuntime) []routeCmd {
	var cmds []routeCmd

	// 1) 出口物理 endpoint 钉物理口（主表 /32），高于策略路由
	if rt.ExitEndpoint != "" && rt.WANDevice != "" {
		gw := rt.WANGateway
		if gw != "" {
			cmds = append(cmds, routeCmd{argv: []string{
				"ip", "route", "replace", rt.ExitEndpoint + "/32", "via", gw, "dev", rt.WANDevice,
			}, optional: true})
		} else {
			cmds = append(cmds, routeCmd{argv: []string{
				"ip", "route", "replace", rt.ExitEndpoint + "/32", "dev", rt.WANDevice,
			}, optional: true})
		}
	}

	// 2) mangle：本机隧道 UDP 出站不打标（先 return 再 mark）
	if snap.Capabilities["nft"] {
		cmds = append(cmds,
			routeCmd{argv: []string{"nft", "add", "chain", "ip", "mangle", "OUTPUT",
				"{ type filter hook output priority -150; }"}, optional: true},
		)
		if rt.ExitEndpoint != "" {
			cmds = append(cmds, routeCmd{argv: []string{
				"nft", "add", "rule", "ip", "mangle", "OUTPUT",
				"ip", "daddr", rt.ExitEndpoint, "return",
			}, optional: true})
		}
		if rt.TunnelLocalPort > 0 {
			cmds = append(cmds, routeCmd{argv: []string{
				"nft", "add", "rule", "ip", "mangle", "OUTPUT",
				"udp", "sport", fmt.Sprintf("%d", rt.TunnelLocalPort), "return",
			}, optional: true})
		}
		// 回环 / 本机解析器
		cmds = append(cmds, routeCmd{argv: []string{
			"nft", "add", "rule", "ip", "mangle", "OUTPUT",
			"oifname", "lo", "return",
		}, optional: true})
		return cmds
	}
	if snap.Capabilities["iptables"] {
		if rt.ExitEndpoint != "" {
			cmds = append(cmds, routeCmd{argv: []string{
				"iptables", "-t", "mangle", "-A", "OUTPUT",
				"-d", rt.ExitEndpoint, "-j", "RETURN",
			}, optional: true})
		}
		if rt.TunnelLocalPort > 0 {
			cmds = append(cmds, routeCmd{argv: []string{
				"iptables", "-t", "mangle", "-A", "OUTPUT",
				"-p", "udp", "--sport", fmt.Sprintf("%d", rt.TunnelLocalPort), "-j", "RETURN",
			}, optional: true})
		}
		cmds = append(cmds, routeCmd{argv: []string{
			"iptables", "-t", "mangle", "-A", "OUTPUT", "-o", "lo", "-j", "RETURN",
		}, optional: true})
		return cmds
	}
	return cmds
}

// proxyServerCmds server/both 侧：IP 转发 + MASQUERADE + FORWARD。
func proxyServerCmds(snap *shared.SystemSnapshot, p *domain.ProxyPolicy, rt ProxyRuntime) []routeCmd {
	if !p.IsServerLike() {
		return nil
	}
	dev := rt.TunDevice
	cmds := []routeCmd{
		{argv: []string{"sysctl", "-w", "net.ipv4.ip_forward=1"}},
	}
	if snap.Capabilities["nft"] {
		cmds = append(cmds,
			routeCmd{argv: []string{"nft", "add", "table", "ip", "nat"}},
			routeCmd{argv: []string{"nft", "add", "chain", "ip", "nat", "POSTROUTING",
				"{ type nat hook postrouting priority 100; }"}, optional: true},
			routeCmd{argv: []string{"nft", "add", "rule", "ip", "nat", "POSTROUTING",
				"iifname", dev, "masquerade"}},
		)
	} else if snap.Capabilities["iptables"] {
		cmds = append(cmds,
			routeCmd{argv: []string{"iptables", "-t", "nat", "-A", "POSTROUTING",
				"-i", dev, "-j", "MASQUERADE"}},
			routeCmd{argv: []string{"iptables", "-A", "FORWARD", "-i", dev, "-j", "ACCEPT"}},
			routeCmd{argv: []string{"iptables", "-A", "FORWARD", "-o", dev, "-j", "ACCEPT"}},
		)
	}
	return cmds
}

// proxyDNSCmds DNS 策略。redirect/resolv 在此；split 写 dnsmasq（单独文件，reload）。
func proxyDNSCmds(p *domain.ProxyPolicy, rt ProxyRuntime) []routeCmd {
	if p.DNS == nil || p.DNS.Mode == "" || p.DNS.Mode == "none" {
		return nil
	}
	switch p.DNS.Mode {
	case "redirect":
		target := p.DNS.RedirectTarget
		if target == "" {
			return nil
		}
		// DNAT :53 → peer:53。链由执行器确保存在。
		return []routeCmd{
			{argv: []string{"nft", "add", "table", "ip", "nat"}},
			{argv: []string{"nft", "add", "chain", "ip", "nat", "PREROUTING",
				"{ type nat hook prerouting priority -100; }"}, optional: true},
			{argv: []string{"nft", "add", "rule", "ip", "nat", "PREROUTING",
				"udp", "dport", "53", "dnat", "to", target + ":53"}},
		}
	case "resolv":
		// 写 resolv.conf 由执行器做（文件写入不是 argv）；此处只报意图
		if p.DNS.Server == "" {
			return nil
		}
		return []routeCmd{{
			argv: []string{"mynet", "proxy", "resolv", p.DNS.Server},
		}}
	case "split":
		// dnsmasq 分流：domestic 默认上游 + 国外域名走 Server
		// 完整 dns_split 见 proxy_dnssplit.go；这里给 reload 入口
		return []routeCmd{{
			argv: []string{"mynet", "proxy", "dns-split",
				strings.Join(p.DNS.DomesticServers, ","), p.DNS.Server},
		}}
	}
	return nil
}

// proxyAllCmds 一次拼齐 client+server+dns+hijack+bypass（apply 用）。
func proxyAllCmds(snap *shared.SystemSnapshot, p *domain.ProxyPolicy, rt ProxyRuntime) []routeCmd {
	var cmds []routeCmd
	cmds = append(cmds, proxyClientCmds(snap, p, rt)...)
	cmds = append(cmds, proxyServerCmds(snap, p, rt)...)
	cmds = append(cmds, proxyDNSCmds(p, rt)...)
	// DNS 强劫持（proxy-split.md §4）：把 :53 收到本地解析器
	if p.DNS != nil && p.DNS.Hijack != "" {
		cmds = append(cmds, DNSHijackCmds(snap, DNSHijackRuntime{
			Scope:     p.DNS.Hijack,
			LANBridge: rt.LANBridge,
		})...)
	}
	// T16 兜底：DoH/853 + 全量进隧道
	cmds = append(cmds, ProxyAllBypassCmds(snap, p, rt)...)
	// T11 出口准入
	cmds = append(cmds, ProxyACLCmds(snap, p, rt)...)
	return cmds
}

// proxyCleanupCmds 禁用时的清理（stop/disable）。del 均 optional。
func proxyCleanupCmds(p *domain.ProxyPolicy, rt ProxyRuntime) []routeCmd {
	if p == nil {
		return nil
	}
	fw := p.EffectiveFwmark()
	table := p.EffectiveTable()
	var cmds []routeCmd
	// 国外 DNS /32
	for _, ip := range DefaultForeignDNSServers {
		cmds = append(cmds, routeCmd{
			argv:     []string{"ip", "route", "del", ip + "/32", "dev", rt.TunDevice},
			optional: true,
		})
	}
	cmds = append(cmds,
		routeCmd{argv: []string{"ip", "rule", "del", "fwmark", fmt.Sprintf("%#x", fw), "lookup", fmt.Sprintf("%d", table)}, optional: true},
		routeCmd{argv: []string{"ip", "route", "flush", "table", fmt.Sprintf("%d", table)}, optional: true},
	)
	return cmds
}

// ── T16：兜底与防绕过 ─────────────────────────────────────────

// DoH 处理模式。
const (
	DoHModeAllow  = "allow"
	DoHModeBlock  = "block"  // 丢弃 DoH/DoT，逼回 :53 劫持
	DoHModeTunnel = "tunnel" // 已知 DoH/DoT 端点钉进隧道
)

// WellKnownDoHIPs 常见 DoH/公共 DNS 端点 IP（853/443 加密 DNS）。
// tunnel 模式下钉进隧道；block 模式下拦 853 + 已知 DoH IP:443。
var WellKnownDoHIPs = []string{
	"8.8.8.8", "8.8.4.4",
	"1.1.1.1", "1.0.0.1",
	"9.9.9.9", "149.112.112.112",
	"208.67.222.222", "208.67.220.220",
}

// FullTunnelCmds 全量进隧道：默认路由/主表指向隧道（保留 WAN /32 修复）。
func FullTunnelCmds(rt ProxyRuntime, table int) []routeCmd {
	if table <= 0 {
		table = domain.DefaultProxyTable
	}
	dev := rt.TunDevice
	cmds := []routeCmd{
		// 已有 policy table 时，再把主表默认也指向隧道（可选模式）
		{argv: []string{"ip", "route", "replace", "default", "dev", dev}},
	}
	if rt.WANGateway != "" && rt.WANDevice != "" {
		cmds = append(cmds, routeCmd{argv: []string{
			"ip", "route", "replace", rt.WANGateway + "/32", "dev", rt.WANDevice,
		}, optional: true})
	}
	return cmds
}

// DoHBypassCmds 加密 DNS 旁路处理。
func DoHBypassCmds(snap *shared.SystemSnapshot, mode string, rt ProxyRuntime) []routeCmd {
	switch mode {
	case DoHModeBlock:
		return dohBlockCmds(snap)
	case DoHModeTunnel:
		return dohTunnelCmds(rt)
	default:
		return nil
	}
}

func dohBlockCmds(snap *shared.SystemSnapshot) []routeCmd {
	var cmds []routeCmd
	if snap.Capabilities["nft"] {
		cmds = append(cmds,
			routeCmd{argv: []string{"nft", "add", "table", "ip", "filter"}},
			routeCmd{argv: []string{"nft", "add", "chain", "ip", "filter", "INPUT",
				"{ type filter hook input priority 0; }"}, optional: true},
			routeCmd{argv: []string{"nft", "add", "chain", "ip", "filter", "FORWARD",
				"{ type filter hook forward priority 0; }"}, optional: true},
			// DoT :853
			routeCmd{argv: []string{"nft", "add", "rule", "ip", "filter", "INPUT",
				"tcp", "dport", "853", "drop"}},
			routeCmd{argv: []string{"nft", "add", "rule", "ip", "filter", "FORWARD",
				"tcp", "dport", "853", "drop"}},
			// 已知 DoH IP 的 443（不能全丢 443，会断网）
		)
		for _, ip := range WellKnownDoHIPs {
			cmds = append(cmds, routeCmd{argv: []string{
				"nft", "add", "rule", "ip", "filter", "INPUT",
				"ip", "daddr", ip, "tcp", "dport", "443", "drop",
			}, optional: true})
		}
		return cmds
	}
	if snap.Capabilities["iptables"] {
		cmds = append(cmds,
			routeCmd{argv: []string{"iptables", "-A", "INPUT", "-p", "tcp", "--dport", "853", "-j", "DROP"}},
			routeCmd{argv: []string{"iptables", "-A", "FORWARD", "-p", "tcp", "--dport", "853", "-j", "DROP"}},
		)
		for _, ip := range WellKnownDoHIPs {
			cmds = append(cmds, routeCmd{argv: []string{
				"iptables", "-A", "INPUT", "-p", "tcp", "-d", ip, "--dport", "443", "-j", "DROP",
			}, optional: true})
		}
		return cmds
	}
	return nil
}

func dohTunnelCmds(rt ProxyRuntime) []routeCmd {
	dev := rt.TunDevice
	var cmds []routeCmd
	// DoT 853 → 隧道
	cmds = append(cmds, routeCmd{argv: []string{
		"ip", "rule", "add", "fwmark", fmt.Sprintf("%#x", domain.DefaultProxyFwmark),
		"lookup", fmt.Sprintf("%d", domain.DefaultProxyTable), "prio", fmt.Sprintf("%d", proxyPrio+1),
	}, optional: true})
	for _, ip := range WellKnownDoHIPs {
		cmds = append(cmds, routeCmd{argv: []string{
			"ip", "route", "replace", ip + "/32", "dev", dev,
		}})
	}
	return cmds
}

// ProxyAllBypassCmds T16 组合：foreign dns /32 + DoH + full tunnel。
func ProxyAllBypassCmds(snap *shared.SystemSnapshot, p *domain.ProxyPolicy, rt ProxyRuntime) []routeCmd {
	if p == nil || !p.Enabled {
		return nil
	}
	var cmds []routeCmd
	// 国外 DNS /32（已有主路径在 proxyClientCmds；此处不重复）
	cmds = append(cmds, DoHBypassCmds(snap, p.DoHMode, rt)...)
	if p.FullTunnel {
		cmds = append(cmds, FullTunnelCmds(rt, p.EffectiveTable())...)
	}
	return cmds
}

// ── T11：出口准入（对谁开放的执行侧）────────────────────────

// ProxyACLCmds 出口侧只放行 AllowedCIDRs（overlay 来源）。
// role=server/both 时生效；allowlist 无 CIDR 则不装（只靠云端业务授权）。
func ProxyACLCmds(snap *shared.SystemSnapshot, p *domain.ProxyPolicy, rt ProxyRuntime) []routeCmd {
	if p == nil || !p.Enabled || !p.IsServerLike() {
		return nil
	}
	if p.ACL == nil || len(p.ACL.AllowedCIDRs) == 0 {
		return nil
	}
	dev := rt.TunDevice
	var cmds []routeCmd
	if snap.Capabilities["nft"] {
		cmds = append(cmds,
			routeCmd{argv: []string{"nft", "add", "table", "ip", "filter"}},
			routeCmd{argv: []string{"nft", "add", "chain", "ip", "filter", "FORWARD",
				"{ type filter hook forward priority 0; }"}, optional: true},
		)
		for _, cidr := range p.ACL.AllowedCIDRs {
			cmds = append(cmds, routeCmd{argv: []string{
				"nft", "add", "rule", "ip", "filter", "FORWARD",
				"iifname", dev, "ip", "saddr", cidr, "accept",
			}, optional: true})
		}
		// 默认丢弃来自隧道且不在名单内的转发
		cmds = append(cmds, routeCmd{argv: []string{
			"nft", "add", "rule", "ip", "filter", "FORWARD",
			"iifname", dev, "drop",
		}, optional: true})
		return cmds
	}
	if snap.Capabilities["iptables"] {
		for _, cidr := range p.ACL.AllowedCIDRs {
			cmds = append(cmds, routeCmd{argv: []string{
				"iptables", "-A", "FORWARD", "-i", dev, "-s", cidr, "-j", "ACCEPT",
			}, optional: true})
		}
		cmds = append(cmds, routeCmd{argv: []string{
			"iptables", "-A", "FORWARD", "-i", dev, "-j", "DROP",
		}, optional: true})
		return cmds
	}
	return cmds
}
