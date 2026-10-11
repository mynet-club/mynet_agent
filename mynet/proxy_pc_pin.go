package mynet

// PC 形态（mac/win）自出口：指定域名解析后把命中 IP 按 /32 钉进隧道（主表）。
//
// 为什么 PC 不能照搬 Linux 的 nft 打标 + 策略表：
//   - PC 没有 fwmark 策略路由表（BSD `route` 无 table 概念，Windows 亦无）；
//   - 铁律（spec/proxy-split.md §15.4）：本机 OUTPUT 打标改不了本机自身的路由。
//
// Linux/OpenWrt **节点**上「本机不可代理自己」；但 PC 上**用户自己就是端点**，
// 没有 LAN 转发可打标 —— 唯一的机制就是主表目的路由。所以 PC 形态按**域名驱动**：
// 用户配哪些域名（无固定 IP 集，可维护小集），就把这些域名解析出的 IP /32 钉进隧道。
//
// 目的集不固定 ⇒ 不装全量 CIDR；规模由域名数决定（`pcPinMaxIPs` 兜底防失控）。

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

// pcPinMaxIPs 小集上限：超出的解析结果被截断。PC 是域名驱动，条目数应与
// 「用户配了几个域名」同阶；几千条说明配置失控或有人在灌全量列表。
const pcPinMaxIPs = 512

// isPCProxyForm 是否 PC 形态（有主表、无策略表）。
func isPCProxyForm(snap *shared.SystemSnapshot) bool {
	return snap != nil && (snap.OS == "darwin" || snap.OS == "windows")
}

// PCProxyPinCmds 把 IP 列表 /32 钉进隧道（主表，PC 方言）。幂等：replace 语义。
// 非 PC 形态返回 nil —— Linux/OpenWrt 走 nft 打标，不在这里动主表。
func PCProxyPinCmds(snap *shared.SystemSnapshot, ips []string, tunDev string) []routeCmd {
	if !isPCProxyForm(snap) || tunDev == "" || len(ips) == 0 {
		return nil
	}
	var cmds []routeCmd
	for _, ip := range dedupStrings(ips) {
		if net.ParseIP(ip) == nil {
			continue
		}
		cmds = append(cmds, routeCmds(snap, RouteOp{
			Action:      "replace",
			Destination: ip + "/32",
			Device:      tunDev,
		})...)
	}
	return cmds
}

// PCProxyPinDelCmds 撤销钉扎（禁用 / 域名失效）。del 一律 optional，幂等。
func PCProxyPinDelCmds(snap *shared.SystemSnapshot, ips []string, tunDev string) []routeCmd {
	if !isPCProxyForm(snap) || tunDev == "" || len(ips) == 0 {
		return nil
	}
	var cmds []routeCmd
	for _, ip := range dedupStrings(ips) {
		if net.ParseIP(ip) == nil {
			continue
		}
		for _, c := range routeCmds(snap, RouteOp{
			Action:      "del",
			Destination: ip + "/32",
			Device:      tunDev,
		}) {
			c.optional = true
			cmds = append(cmds, c)
		}
	}
	return cmds
}

// ResolvePCProxyPinIPs 解析「要代理的域名」→ IPv4 集（去重、排序、封顶）。
// resolve 可注入（测试）；nil 时用系统解析器（短超时）。
func ResolvePCProxyPinIPs(rules *domain.ProxyDomainRules, resolve func(string) []string, max int) []string {
	if rules == nil {
		return nil
	}
	if resolve == nil {
		resolve = lookupHostV4
	}
	if max <= 0 {
		max = pcPinMaxIPs
	}
	seen := map[string]bool{}
	var out []string
	add := func(domains []string) {
		for _, raw := range domains {
			d := normalizeDomainName(raw)
			if d == "" {
				continue
			}
			for _, ip := range resolve(d) {
				if net.ParseIP(ip) == nil || seen[ip] {
					continue
				}
				seen[ip] = true
				out = append(out, ip)
				if len(out) >= max {
					return
				}
			}
			if len(out) >= max {
				return
			}
		}
	}
	// 代理域名 = 基础清单 ∪ 自定义 − 直连（D5 合并组）；clean 列表同样走隧道。
	add(rules.EffectiveProxied())
	add(rules.UpstreamClean)
	sort.Strings(out)
	return out
}

// lookupHostV4 系统解析器取 A 记录（短超时，失败返回 nil 不阻断 apply）。
func lookupHostV4(domain string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, domain)
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		if v4 := a.IP.To4(); v4 != nil {
			out = append(out, v4.String())
		}
	}
	return out
}

// pcPinFilePath 上次钉扎的 IP 清单（跨 apply 对账 + 禁用清理）。
func pcPinFilePath() string {
	return filepath.Join(varDir(), "proxy", "pc-pin.json")
}

// LoadPCPinIPs 读上次钉扎清单（缺失/损坏 → 空，不报错）。
func LoadPCPinIPs() []string {
	raw, err := os.ReadFile(pcPinFilePath())
	if err != nil {
		return nil
	}
	var body struct {
		IPs []string `json:"ips"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	return body.IPs
}

// SavePCPinIPs 写钉扎清单（空则删文件）。
func SavePCPinIPs(ips []string) error {
	p := pcPinFilePath()
	if len(ips) == 0 {
		_ = os.Remove(p)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"ips": ips})
	return os.WriteFile(p, raw, 0o644)
}

// diffStrings a 中不在 b 的元素。
func diffStrings(a, b []string) []string {
	bs := make(map[string]bool, len(b))
	for _, s := range b {
		bs[s] = true
	}
	var out []string
	for _, s := range a {
		if !bs[s] {
			out = append(out, s)
		}
	}
	return out
}

// ApplyPCPin 对账：先撤失效钉扎，再钉当前域名解析集。返回待执行命令 + 新清单。
// 调用方逐条 run，成功后 SavePCPinIPs(next)。非 PC 形态返回 (nil, nil)。
func ApplyPCPin(snap *shared.SystemSnapshot, rules *domain.ProxyDomainRules, tunDev string) ([]routeCmd, []string) {
	if !isPCProxyForm(snap) {
		return nil, nil
	}
	prev := LoadPCPinIPs()
	next := ResolvePCProxyPinIPs(rules, nil, pcPinMaxIPs)
	var cmds []routeCmd
	cmds = append(cmds, PCProxyPinDelCmds(snap, diffStrings(prev, next), tunDev)...)
	cmds = append(cmds, PCProxyPinCmds(snap, next, tunDev)...)
	return cmds, next
}
