package mynet

// L5 驱动侧放行（wg 方言）：把 proxy 出口目的集并进出口 peer 的 AllowedIPs。
//
// 为什么需要这一层：WireGuard 的 AllowedIPs 是**双向 cryptokey routing**，不只是
// 系统路由。OS 侧 nft 打标 + `ip route … table <N> dev wg0` 把包塞进 wg 设备后，
// wg 自己还要按 AllowedIPs 做一次「这个目的地址归谁」的查表；目的集不在任何 peer
// 的 AllowedIPs 里 → 包被 wg **静默丢弃**，现象是「策略路由装好了、集合也灌了，
// 流量就是出不去」。这就是 spec/proxy-split.md §15.1「只打标不放行 → 进了也丢」。
//
// 于是放行（AllowedIPs ⊇ 目的集）与打标（nft → table）缺一不可：
//   - 只打标不放行 → wg 丢包（本文件补的正是这条）；
//   - 只放行不打标 → 目的集进不了 table（本机自身流量另有铁律，spec §15.4）。
//
// 幂等：目的集并集去重后写入；每次从 bundle 重渲染，同一输入得同一份。
// fail-loud：投影后若「目的集 ⊄ 所有 peer 的 AllowedIPs 并集」，说明出口 peer 在
// wg bundle 里根本不存在（选点对不上）—— 拒绝写盘并报错，不静默留一份会丢包的配置。

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// ProjectProxyToWG 把 proxy 出口目的集并进匹配出口的 wg peer 的 AllowedIPs，
// 并关闭 wg-quick 的自动路由（Table = off）。仅对 client/both 且 enabled 生效。
//
// 调用方须先判「适用」（enabled && IsClientLike && 有出口）；不适用直接 no-op。
func ProjectProxyToWG(w *domain.WireGuardIntent, p *domain.ProxyPolicy, destCIDRs []string) error {
	if w == nil || p == nil || !p.Enabled || !p.IsClientLike() {
		return nil
	}
	exits := enabledExitIDs(p)
	if len(exits) == 0 {
		return nil
	}
	if len(destCIDRs) == 0 {
		return fmt.Errorf("proxy 目的集为空：无法确定出口 peer 的 AllowedIPs（拒绝写一份会丢包的 wg 配置）")
	}

	for i := range w.Peers {
		if !peerMatchesAnyExit(&w.Peers[i], exits) {
			continue
		}
		w.Peers[i].AllowedIPs = unionCIDRStrings(w.Peers[i].AllowedIPs, destCIDRs)
	}

	// Table = off：代理接管路由（打标 + table <N>），**禁止** wg-quick 把目的集写进主表。
	// 主表路由会连**本机自身**发往这些目的的流量一起卷进隧道 —— 正是 spec §15.4
	// 已下线的「主表 /32 钉扎」病。所以放行必须与接管路由成对出现。
	if w.Routing == nil {
		w.Routing = &domain.WgRouting{}
	}
	w.Routing.Mode = wgRoutingOff

	return verifyWGDestCovered(w, destCIDRs, exits)
}

// wgRoutingOff 让 wg-quick 不碰系统路由的模式值（RenderWGConfig 渲染成 `Table = off`）。
const wgRoutingOff = "off"

// WGProxyDestCIDRs 解算「需要从出口 peer 出去的 proxy 目的集」。
//
//	FullTunnel / inverted → 0.0.0.0/0（wg 表达不了补集，取安全超集；真正进不进隧道
//	                        仍由 OS 打标决定，Table = off 下不会因此劫持主表）
//	normal               → 列表文件里的 CIDR（随包列表优先，其次节点缓存）
//
// 返回 error = 目的集无法确定（缺列表文件等）。**不静默给空集**：空集会让 fail-loud 假绿。
func WGProxyDestCIDRs(p *domain.ProxyPolicy) ([]string, error) {
	if p == nil {
		return nil, nil
	}
	if p.FullTunnel {
		return []string{"0.0.0.0/0"}, nil
	}
	if p.Split == nil {
		return nil, fmt.Errorf("proxy 策略缺 split（region=%s policy=%s）：无法解算目的集", p.Region, p.Policy)
	}
	if p.Split.MatchMode == domain.ProxyMatchInverted {
		return []string{"0.0.0.0/0"}, nil
	}
	f := resolveWGProxyListFile(p)
	if f == "" {
		return nil, fmt.Errorf("proxy 目的集列表文件缺失（region=%s policy=%s）：随包与缓存都没找到",
			p.Region, p.Policy)
	}
	data, err := os.ReadFile(f)
	if err != nil {
		return nil, fmt.Errorf("读 proxy 列表 %s: %w", f, err)
	}
	cidrs := parseCIDRLines(string(data))
	if len(cidrs) == 0 {
		return nil, fmt.Errorf("proxy 列表 %s 无有效 CIDR 行", f)
	}
	return cidrs, nil
}

// resolveWGProxyListFile 目的集列表文件：随包列表优先，其次节点缓存。
// 与 planFromBundle 同一套来源（bundledProxyListPath / ProxyListCachePath）。
func resolveWGProxyListFile(p *domain.ProxyPolicy) string {
	if f := bundledProxyListPath(p); f != "" {
		return f
	}
	src := p.Split.ListSource
	if p.Split.IPList != nil && p.Split.IPList.Source != "" {
		src = p.Split.IPList.Source
	}
	if src == "" {
		return ""
	}
	f := ProxyListCachePath(filepath.Join(varDir(), "proxy-lists"), src)
	if _, err := os.Stat(f); err != nil {
		return ""
	}
	return f
}

// parseCIDRLines 解析 CIDR 列表文本（空行/`#` 注释跳过）。
func parseCIDRLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// enabledExitIDs 生效出口的节点 ID 集合。
func enabledExitIDs(p *domain.ProxyPolicy) map[string]bool {
	m := map[string]bool{}
	for _, e := range p.Exits {
		if e.Enabled && e.NodeID != "" {
			m[e.NodeID] = true
		}
	}
	return m
}

// peerMatchesAnyExit peer 是否就是某个生效出口（业务节点 ID 或 peer ID 命中）。
func peerMatchesAnyExit(peer *domain.WgPeer, exits map[string]bool) bool {
	if peer == nil {
		return false
	}
	if peer.BusinessNodeID != "" && exits[peer.BusinessNodeID] {
		return true
	}
	return peer.ID != "" && exits[peer.ID]
}

// unionCIDRStrings 并集：base 原序保留，再把 extra 中缺失的追加（按精确串去重，幂等）。
func unionCIDRStrings(base, extra []string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, s := range base {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range extra {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// verifyWGDestCovered fail-loud：目的集必须被所有 peer 的 AllowedIPs 并集覆盖。
// 不覆盖 = 有出口选点对不上任何 wg peer（选了不存在的出口）→ 报错，不静默丢包。
func verifyWGDestCovered(w *domain.WireGuardIntent, destCIDRs []string, exits map[string]bool) error {
	var allowed []*net.IPNet
	for i := range w.Peers {
		for _, a := range w.Peers[i].AllowedIPs {
			if n := parseIPNet(a); n != nil {
				allowed = append(allowed, n)
			}
		}
	}
	var missing []string
	for _, d := range destCIDRs {
		dn := parseIPNet(d)
		if dn == nil {
			return fmt.Errorf("proxy 目的集含有非法 CIDR %q", d)
		}
		covered := false
		for _, a := range allowed {
			if cidrCovers(a, dn) {
				covered = true
				break
			}
		}
		if !covered {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		ids := make([]string, 0, len(exits))
		for id := range exits {
			ids = append(ids, id)
		}
		return fmt.Errorf("proxy 放行失败：目的集有 %d 条不被任何 peer 的 AllowedIPs 覆盖"+
			"（出口 %v 在 wg bundle 里对不上 peer？）示例: %s",
			len(missing), ids, strings.Join(headN(missing, 3), ", "))
	}
	return nil
}

// cidrCovers outer 是否覆盖 inner（同族、前缀不长于 inner、含 inner 网络地址）。
func cidrCovers(outer, inner *net.IPNet) bool {
	oOnes, oBits := outer.Mask.Size()
	iOnes, iBits := inner.Mask.Size()
	if oBits != iBits || oOnes > iOnes {
		return false
	}
	return outer.Contains(inner.IP)
}

// parseIPNet 解析 "1.2.3.0/24" 或裸 "1.2.3.4"（视为 /32）。
func parseIPNet(s string) *net.IPNet {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if !strings.Contains(s, "/") {
		ip := net.ParseIP(s)
		if ip == nil || ip.To4() == nil {
			return nil
		}
		return &net.IPNet{IP: ip.To4(), Mask: net.CIDRMask(32, 32)}
	}
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return nil
	}
	return n
}

func headN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
