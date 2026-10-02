package mynet

// DNS 强劫持（入口职责，proxy-split.md §4）。
//
// 与旧线 dns_mode=redirect 的区别：
//   - redirect：把 :53 DNAT 到**出口节点**（peer:53）
//   - hijack：把 :53 收到**本地解析器**，由本地 dnsmasq 按域名规则选上游
//
// 范围：
//   - local：只劫本机（PC，消费者=入口）
//   - lan：劫 LAN + 本机（网关）
//
// ⚠️ 必须放行解析器自己的上游查询，否则 dnsmasq 查 8.8.8.8 会被劫回自己 → 死循环。

import (
	"fmt"

	"github.com/mynet-club/mynet_agent/shared"
)

// DNS hijack 范围。
const (
	DNSHijackOff   = "off"
	DNSHijackLocal = "local" // 仅本机 lo
	DNSHijackLAN   = "lan"   // LAN + 本机（网关）
)

// DNSHijackRuntime 劫持所需的接口上下文。
type DNSHijackRuntime struct {
	// Scope off|local|lan
	Scope string
	// LANBridge 网关场景的 LAN 口（br-lan / eth0）。scope=lan 必填。
	LANBridge string
	// ResolverUser 本地解析器进程用户（默认 dnsmasq），用于放行上游查询。
	ResolverUser string
	// Port 本地解析器端口，默认 53。
	Port int
}

func (r DNSHijackRuntime) resolverUser() string {
	if r.ResolverUser == "" {
		return "dnsmasq"
	}
	return r.ResolverUser
}

func (r DNSHijackRuntime) port() string {
	if r.Port <= 0 {
		return "53"
	}
	return fmt.Sprintf("%d", r.Port)
}

// DNSHijackCmds 生成强劫持命令。
//
// nft 路径：
//
//	OUTPUT   → 本机 App 的 DNS
//	PREROUTING（iifname LAN）→ 网关下游
//
// iptables 路径同语义（REDIRECT）。
func DNSHijackCmds(snap *shared.SystemSnapshot, rt DNSHijackRuntime) []routeCmd {
	if rt.Scope == DNSHijackOff || rt.Scope == "" {
		return nil
	}
	user := rt.resolverUser()
	port := rt.port()
	var cmds []routeCmd

	if snap.Capabilities["nft"] {
		// nat 表与 OUTPUT/PREROUTING 链
		cmds = append(cmds,
			routeCmd{argv: []string{"nft", "add", "table", "ip", "nat"}},
			routeCmd{argv: []string{"nft", "add", "chain", "ip", "nat", "OUTPUT",
				"{ type nat hook output priority -100; }"}, optional: true},
		)

		// ① 放行解析器自己的上游查询（防自环）—— 必须在 redirect 之前
		cmds = append(cmds,
			routeCmd{argv: []string{
				"nft", "add", "rule", "ip", "nat", "OUTPUT",
				"meta", "skuid", user, "udp", "dport", "53", "accept",
			}, optional: true},
			routeCmd{argv: []string{
				"nft", "add", "rule", "ip", "nat", "OUTPUT",
				"meta", "skuid", user, "tcp", "dport", "53", "accept",
			}, optional: true},
			// 已经是本机解析器的查询不再劫（redirect 到自己无意义）
			routeCmd{argv: []string{
				"nft", "add", "rule", "ip", "nat", "OUTPUT",
				"ip", "daddr", "127.0.0.1", "udp", "dport", "53", "accept",
			}, optional: true},
		)

		// ② 本机劫持
		cmds = append(cmds,
			routeCmd{argv: []string{
				"nft", "add", "rule", "ip", "nat", "OUTPUT",
				"udp", "dport", "53", "redirect", "to", ":" + port,
			}},
			routeCmd{argv: []string{
				"nft", "add", "rule", "ip", "nat", "OUTPUT",
				"tcp", "dport", "53", "redirect", "to", ":" + port,
			}},
		)

		// ③ 网关：劫 LAN 下游
		if rt.Scope == DNSHijackLAN && rt.LANBridge != "" {
			cmds = append(cmds,
				routeCmd{argv: []string{
					"nft", "add", "chain", "ip", "nat", "PREROUTING",
					"{ type nat hook prerouting priority -100; }"}, optional: true},
				routeCmd{argv: []string{
					"nft", "add", "rule", "ip", "nat", "PREROUTING",
					"iifname", rt.LANBridge, "udp", "dport", "53", "redirect", "to", ":" + port,
				}},
				routeCmd{argv: []string{
					"nft", "add", "rule", "ip", "nat", "PREROUTING",
					"iifname", rt.LANBridge, "tcp", "dport", "53", "redirect", "to", ":" + port,
				}},
			)
		}
		return cmds
	}

	if snap.Capabilities["iptables"] {
		// OUTPUT：放行解析器 → REDIRECT
		cmds = append(cmds,
			routeCmd{argv: []string{
				"iptables", "-t", "nat", "-A", "OUTPUT",
				"-p", "udp", "--dport", "53", "-m", "owner", "--uid-owner", user, "-j", "ACCEPT",
			}, optional: true},
			routeCmd{argv: []string{
				"iptables", "-t", "nat", "-A", "OUTPUT",
				"-p", "tcp", "--dport", "53", "-m", "owner", "--uid-owner", user, "-j", "ACCEPT",
			}, optional: true},
			routeCmd{argv: []string{
				"iptables", "-t", "nat", "-A", "OUTPUT",
				"-p", "udp", "--dport", "53", "-j", "REDIRECT", "--to-ports", port,
			}},
			routeCmd{argv: []string{
				"iptables", "-t", "nat", "-A", "OUTPUT",
				"-p", "tcp", "--dport", "53", "-j", "REDIRECT", "--to-ports", port,
			}},
		)
		if rt.Scope == DNSHijackLAN && rt.LANBridge != "" {
			cmds = append(cmds,
				routeCmd{argv: []string{
					"iptables", "-t", "nat", "-A", "PREROUTING",
					"-i", rt.LANBridge, "-p", "udp", "--dport", "53", "-j", "REDIRECT", "--to-ports", port,
				}},
				routeCmd{argv: []string{
					"iptables", "-t", "nat", "-A", "PREROUTING",
					"-i", rt.LANBridge, "-p", "tcp", "--dport", "53", "-j", "REDIRECT", "--to-ports", port,
				}},
			)
		}
		return cmds
	}
	return nil
}

// DNSHijackCleanupCmds 清理劫持（optional del）。
func DNSHijackCleanupCmds(snap *shared.SystemSnapshot, rt DNSHijackRuntime) []routeCmd {
	// 精确删规则成本高；flush 指定链上我们加的 redirect 即可。
	// 这里删 53 相关 redirect/accept，失败可忽略。
	if snap.Capabilities["nft"] {
		return []routeCmd{
			{argv: []string{"nft", "flush", "chain", "ip", "nat", "OUTPUT"}, optional: true},
			{argv: []string{"nft", "flush", "chain", "ip", "nat", "PREROUTING"}, optional: true},
		}
	}
	if snap.Capabilities["iptables"] {
		return []routeCmd{
			{argv: []string{"iptables", "-t", "nat", "-F", "OUTPUT"}, optional: true},
			{argv: []string{"iptables", "-t", "nat", "-F", "PREROUTING"}, optional: true},
		}
	}
	return nil
}
