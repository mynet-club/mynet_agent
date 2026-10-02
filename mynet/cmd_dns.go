package mynet

// mynet dns —— DNS 三选一落地与紧急恢复（spec §13）。
//
//	mynet dns status                       查看接管状态与备份
//	mynet dns restore                      紧急还原系统 DNS（只读备份，离线可用）
//	mynet dns apply [--mode peer|smart|peer_smart] [--peer VIP]... [--smart ADDR]...
//	mynet dns apply [target]               单上游便捷（旧用法）
//
// 三选一：① 隧道节点 DNS（peer VIP，走加密隧道，安全）
//        ② 智能 DNS（公网/自建，兜底不断网）
//        ③ 不加速（默认，不碰 DNS）
// ①+② 叠加（peer_smart）：peer 优先，smart 兜底 —— nameserver 顺序 fallback。
//
// restore 是「干死 DNS」时的救命命令：只读 var/dns-backup.json，
// 不做网络探测、不需要 mynetd 在跑。

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

func cmdDNS(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet dns status | restore | apply [--mode …] [--peer VIP]... [--smart ADDR]...")
		return 2
	}
	switch args[0] {
	case "status":
		return dnsStatus()
	case "restore":
		return dnsRestore()
	case "apply":
		return dnsApplyArgs(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "dns: 未知子命令 %q\n", args[0])
		return 2
	}
}

// dnsApplyArgs 解析三选一参数并落地。
// 兼容旧用法 `mynet dns apply <target>`（单上游）。
func dnsApplyArgs(args []string) int {
	var mode string
	var peers, smarts []string
	target := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--mode":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "dns apply: --mode 需要 peer|smart|peer_smart")
				return 2
			}
			mode = args[i+1]
			i++
		case "--peer":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "dns apply: --peer 需要 VIP")
				return 2
			}
			peers = append(peers, args[i+1])
			i++
		case "--smart":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "dns apply: --smart 需要地址")
				return 2
			}
			smarts = append(smarts, args[i+1])
			i++
		default:
			// 旧用法：单个 target
			if target == "" && mode == "" && len(peers) == 0 && len(smarts) == 0 {
				target = args[i]
			} else {
				fmt.Fprintf(os.Stderr, "dns apply: 未知参数 %q\n", args[i])
				return 2
			}
		}
	}

	// 旧用法：`dns apply <target>`
	if target != "" && mode == "" {
		if err := ApplySystemDNS("", target); err != nil {
			fmt.Fprintf(os.Stderr, "✗ 接管失败: %v\n", err)
			return 1
		}
		b := LoadDNSBackup()
		if b != nil {
			fmt.Printf("✓ 系统 DNS 已接管 → %s\n", b.Target)
		}
		return 0
	}

	// 三选一：无 --mode 时按参数推断
	if mode == "" {
		switch {
		case len(peers) > 0 && len(smarts) > 0:
			mode = domain.DNSModePeerSmart
		case len(peers) > 0:
			mode = domain.DNSModePeer
		case len(smarts) > 0:
			mode = domain.DNSModeSmart
		default:
			fmt.Fprintln(os.Stderr, "dns apply: 需要 --peer/--smart 或 <target>")
			return 2
		}
	}

	d := &domain.ProxyDNS{Mode: mode}
	for _, p := range peers {
		d.PeerServers = append(d.PeerServers, domain.DNSServer{Source: "self", Addr: p})
	}
	for _, s := range smarts {
		d.SmartServers = append(d.SmartServers, domain.DNSServer{Source: "self", Addr: s})
	}

	if !d.Active() {
		fmt.Println("dns: mode=off 或无可用上游，不碰 DNS")
		return 0
	}
	if err := ApplyProxyDNSPlan("", d); err != nil {
		fmt.Fprintf(os.Stderr, "✗ 接管失败: %v\n", err)
		return 1
	}
	b := LoadDNSBackup()
	if b != nil {
		fmt.Printf("✓ 系统 DNS 已接管（%s）→ %s\n", mode, b.Target)
	}
	return 0
}

func dnsStatus() int {
	b := LoadDNSBackup()
	if b == nil {
		fmt.Println("dns: 无接管记录（var/dns-backup.json 不存在）")
		return 0
	}
	out, _ := json.MarshalIndent(b, "", "  ")
	fmt.Println(string(out))
	if b.Applied {
		fmt.Println("⚠ 已接管未还原 —— mynet dns restore 可紧急恢复")
	} else {
		fmt.Println("✓ 无未还原的接管")
	}
	return 0
}

// dnsRestore 紧急还原（保险丝③）：离线可用，只读备份。
func dnsRestore() int {
	// 恢复语义（用户定案）：**默认回 DHCP 自动获取**。
	// 只有备份里明确记了「接管前是静态 DNS」才还原成那个值；
	// 接管前是 DHCP（servers 空）→ 一律 DHCP；无备份 → 也 DHCP（安全默认）。
	b := LoadDNSBackup()
	if b != nil && b.Applied && len(b.Servers) > 0 {
		if err := RestoreSystemDNS(); err != nil {
			fmt.Fprintf(os.Stderr, "✗ 还原失败: %v\n", err)
			return 1
		}
		fmt.Printf("✓ 系统 DNS 已还原 → %v\n", b.Servers)
		return 0
	}
	// DHCP 路径：不管有没有备份，都设 DHCP
	if err := writeSystemDNS("", nil); err != nil {
		fmt.Fprintf(os.Stderr, "✗ 设 DHCP 失败: %v\n", err)
		return 1
	}
	// 标记已还原
	if b != nil {
		b.Applied = false
		b.RestoredAt = time.Now().UTC().Format(time.RFC3339)
		_ = saveDNSBackup(b)
	}
	fmt.Println("✓ 系统 DNS 已恢复 DHCP 自动获取")
	return 0
}
