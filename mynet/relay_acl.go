package mynet

import (
	"net"
	"sort"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// relayACL 两跳中转的准入与运行参数（由云端 ACL 与驱动意图派生，默认 open）。
type relayACL struct {
	Enabled      bool
	Mode         string
	Allow        []string
	Deny         []string
	Dev          string
	OverlayCIDRs []string
}

// deriveRelayACLForIntent 结合 GnbIntent 派生两跳 ACL 与接口/overlay 网段参数。
func deriveRelayACLForIntent(pp *domain.PluginProxyConfig, perm *domain.ProxyPermission, gi *domain.GnbIntent) relayACL {
	if gi == nil {
		return deriveRelayACL(pp, perm, nil)
	}
	out := deriveRelayACL(pp, perm, gi.Nodes)
	if !out.Enabled {
		return out
	}
	if gi.Options != nil {
		dev := strings.TrimSpace(gi.Options.Ifname)
		if dev != "" && domain.ValidateTunDevice(dev) == nil {
			out.Dev = dev
		}
	}
	out.OverlayCIDRs = deriveOverlayCIDRs(gi)
	return out
}

// deriveOverlayCIDRs 提取本节点及同网 overlay 的 IPv4 CIDR（用于 2hop 排除内网互访）。
func deriveOverlayCIDRs(gi *domain.GnbIntent) []string {
	if gi == nil {
		return nil
	}
	var raw []string
	for _, n := range gi.Nodes {
		s := strings.TrimSpace(n.TunAddress)
		if s == "" {
			continue
		}
		ip, ipNet, err := net.ParseCIDR(s)
		if err != nil || ip.To4() == nil {
			continue
		}
		raw = append(raw, ipNet.String())
	}
	localID := strings.TrimSpace(gi.Identity.NodeID)
	for _, r := range gi.Routes {
		if localID != "" && strings.TrimSpace(r.NodeID) != localID {
			continue
		}
		s := strings.TrimSpace(r.Prefix)
		ip, ipNet, err := net.ParseCIDR(s)
		if err != nil || ip.To4() == nil {
			continue
		}
		raw = append(raw, ipNet.String())
	}
	return dedupeSort(raw)
}

// deriveRelayACL 把代理插件意图 + 云端出口许可 + 节点表合成为 2hop ACL。
//
// 启用条件：本节点是 role both 且至少一个可用出口（既做出入口又回压下一跳）。
// ACL 语义与产品一致：ACL 为空 = open；allowlist/denylist 的 node_id 用节点表
// 翻译成 overlay VIP(/32)，CIDR 只接受合法 IPv4。
func deriveRelayACL(pp *domain.PluginProxyConfig, perm *domain.ProxyPermission, nodes []domain.GnbNode) relayACL {
	out := relayACL{Mode: domain.ProxyACLOpen}
	if pp == nil || !strings.EqualFold(pp.Role, "both") || len(pp.EffectiveExits()) == 0 {
		return out
	}
	out.Enabled = true
	if perm == nil || perm.ACL == nil {
		return out
	}
	acl := perm.ACL
	switch acl.EffectiveMode() {
	case domain.ProxyACLAllowlist:
		out.Mode = domain.ProxyACLAllowlist
		out.Allow = append(out.Allow, validCIDRs(acl.AllowedCIDRs)...)
		out.Allow = append(out.Allow, nodeVIPs(nodes, acl.AllowedNodeIDs)...)
		out.Deny = nodeVIPs(nodes, acl.DeniedNodeIDs)
	case domain.ProxyACLDenylist:
		out.Mode = domain.ProxyACLDenylist
		out.Deny = nodeVIPs(nodes, acl.DeniedNodeIDs)
	default:
		out.Mode = domain.ProxyACLOpen
	}
	out.Allow = dedupeSort(out.Allow)
	out.Deny = dedupeSort(out.Deny)
	return out
}

// nodeVIPs 把节点 ID 翻译为该节点的 overlay VIP /32；未知节点忽略。
func nodeVIPs(nodes []domain.GnbNode, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[strings.TrimSpace(id)] = true
	}
	var out []string
	for _, n := range nodes {
		if !want[strings.TrimSpace(n.NodeID)] {
			continue
		}
		ip, _, err := net.ParseCIDR(strings.TrimSpace(n.TunAddress))
		if err != nil || ip.To4() == nil {
			continue
		}
		out = append(out, ip.String()+"/32")
	}
	return out
}

// validCIDRs 只保留合法 IPv4 CIDR，避免脏值进脚本/iptables。
func validCIDRs(in []string) []string {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		ip, _, err := net.ParseCIDR(s)
		if err != nil || ip.To4() == nil {
			continue
		}
		out = append(out, s)
	}
	return out
}

func dedupeSort(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
