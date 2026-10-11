package mynet

// L5 驱动侧放行（wg 方言）：把 proxy 出口**目的集**并进出口 peer 的 AllowedIPs。
//
// 先澄清一件事：WireGuard 的 `AllowedIPs` 是**双向 cryptokey routing**（发侧按目的地址
// 选 peer + 收侧防伪），**不是分流选择器**。分流选择器是我们那层：nft set + `mark <fwmark>`
// + `table <N>`。所以 AllowedIPs 只要做那个集合的**超集**（别让 wg 把包丢了）即可 ——
// **不要**把 split 列表的几万条 CIDR 抄进来：
//   - 形态上没必要（选择在 nft）；
//   - 代价上荒谬（interip 列表 ~1.8 万条，conf 变 1.8 万行，每次 syncconf 都要同步）；
//   - 语义上做不到（inverted「未命中列表即代理」的目标集是**补集**，CIDR 表达不了）。
//
// 于是本文件的映射（与 gnb 的 proxy.conf **同源**：默认出口 / 分组 target→出口）：
//   默认出口（`[default]` / EffectiveExits）→ `0.0.0.0/0`：wg 里没有「兜底出口」，
//     唯一等价表达就是全放行；真正进不进隧道由 nft 打标决定。
//   分组出口（`[group] target <cidr> exit`）→ 该组 targets 的并集：wg 按**最长前缀**
//     在 peer 之间选（0.0.0.0/0 兜底 + 组内窄前缀），与 gnb 的 group 语义等价。
//   组内无出口 = DROP（gnb 语义）→ 不给任何 peer 放行，wg 自然丢弃。
//
// 幂等：并集去重；每次从 bundle 重渲染，同一输入同一份。
// fail-loud：某个出口**配了目的集**却在 wg bundle 里找不到对应 peer ⇒ 报错不写盘
// （选点对不上 = 静默丢包，宁可起不来也要看得见）。

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// wgRoutingOff 让 wg-quick 不碰系统路由的模式值（RenderWGConfig 渲染成 `Table = off`）。
const wgRoutingOff = "off"

// wgGlobalDest 默认出口的兜底目的集。见文件头：wg 没有「兜底出口」，只能全放行。
const wgGlobalDest = "0.0.0.0/0"

// WGExitDestinations 从 proxy 插件配置（与 gnb 投影同一份来源）解算「出口节点 ID → 目的集」。
func WGExitDestinations(c *domain.PluginProxyConfig) map[string][]string {
	out := map[string][]string{}
	if c == nil {
		return out
	}
	// 默认出口：[default] 兜底 → 全放行
	for _, e := range c.EffectiveExits() {
		out[e.NodeID] = append(out[e.NodeID], wgGlobalDest)
	}
	// 分组出口：该组 targets → 组内出口（最长前缀在 wg 侧生效）
	for _, g := range c.Groups {
		var targets []string
		for _, t := range g.Targets {
			t = strings.TrimSpace(t)
			// 与 gnb 侧同口径：/0 拒载（由 [default] 兜底表达）
			if t == "" || t == "0.0.0.0/0" || t == "0/0" {
				continue
			}
			targets = append(targets, t)
		}
		// 组内无出口 = DROP：不写进任何 peer 的 AllowedIPs（wg 自然丢）
		if len(targets) == 0 {
			continue
		}
		for _, e := range g.Exits {
			if e.Enabled && e.NodeID != "" {
				out[e.NodeID] = append(out[e.NodeID], targets...)
			}
		}
	}
	for id := range out {
		out[id] = dedupStrings(out[id])
	}
	return out
}

// ProjectProxyToWG 把「出口 → 目的集」并进匹配出口的 wg peer 的 AllowedIPs，并关闭
// wg-quick 的自动路由（`Table = off`）。调用方须先判「适用」（enabled && client-like）。
func ProjectProxyToWG(w *domain.WireGuardIntent, destByExit map[string][]string) error {
	if w == nil || len(destByExit) == 0 {
		return nil
	}
	ids := make([]string, 0, len(destByExit))
	for id := range destByExit {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	matched := map[string]bool{}
	for i := range w.Peers {
		for _, id := range ids {
			if !peerMatchesExit(&w.Peers[i], id) {
				continue
			}
			w.Peers[i].AllowedIPs = unionCIDRStrings(w.Peers[i].AllowedIPs, destByExit[id])
			matched[id] = true
		}
	}

	// fail-loud：配了目的集的出口在 wg bundle 里对不上 peer（选点错 = 静默丢包）
	var missing []string
	for _, id := range ids {
		if !matched[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("proxy 放行失败：出口 %v 在 wg bundle 里对不上 peer"+
			"（AllowedIPs 放行不了 ⇒ 包会被 wg cryptokey routing 静默丢弃）", missing)
	}

	// Table = off：代理接管路由（打标 + table <N>），**禁止** wg-quick 按 AllowedIPs 往主表写路由
	// —— 尤其 0.0.0.0/0 会把默认路由抢走，连本机自身流量一并卷进隧道（spec §15.4 已下线的病）。
	if w.Routing == nil {
		w.Routing = &domain.WgRouting{}
	}
	w.Routing.Mode = wgRoutingOff

	return nil
}

// peerMatchesExit peer 是否就是某个出口（业务节点 ID 或 peer ID 命中）。
func peerMatchesExit(peer *domain.WgPeer, exitID string) bool {
	if peer == nil || exitID == "" {
		return false
	}
	if peer.BusinessNodeID != "" && peer.BusinessNodeID == exitID {
		return true
	}
	return peer.ID != "" && peer.ID == exitID
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
