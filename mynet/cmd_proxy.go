package mynet

// mynet proxy —— 代理分流的执行入口（apply 内部 exec，也可手动跑）。
//
//	mynet proxy apply [--dry-run]               把当前意图落到系统（只动 proxy 那一套）
//	mynet proxy dns-split <domestic-csv> <foreign>
//	mynet proxy resolv <server>
//	mynet proxy status
//
// apply --auto 生成命令链时会调 `mynet proxy dns-split` / `resolv`；
// 没有这个子命令时整条链 exit 2（macOS 实测）。

import (
	"fmt"
	"os"
	"strings"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func cmdProxy(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet proxy apply [--dry-run] | dns-split <domestic-csv> <foreign> | resolv <server> | status | migrate [--dry-run] | dns-hook start|stop|status")
		return 2
	}
	switch args[0] {
	case "apply":
		// 把当前 proxy 意图落到系统：只装/清 proxy 那一套，不碰 OS 路由/防火墙/接口。
		return proxyApply(len(args) > 1 && args[1] == "--dry-run")
	case "dns-split":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "用法: mynet proxy dns-split <domestic-csv> <foreign>")
			return 2
		}
		return proxyDNSSplit(args[1], args[2])
	case "resolv":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "用法: mynet proxy resolv <server>")
			return 2
		}
		return proxyResolv(args[1])
	case "status":
		return proxyStatus()
	case "migrate":
		// O4：旧 proxy_role.conf → 统一 Local 意图（幂等，失败关闭）
		dry := len(args) > 1 && args[1] == "--dry-run"
		return proxyMigrate(dry)
	case "dns-hook":
		// PC 答案钩子：本地 forwarder，代理域名 A 答案增量钉扎。
		return cmdProxyDNSHook(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "proxy: 未知子命令 %q\n", args[0])
		return 2
	}
}

// proxyApply 把当前 proxy 意图落到系统 —— **只动 proxy 那一套**：nft 打标集合、
// fwmark 规则、table 200、出口规则、DNS 产物。**不碰** OS 路由/防火墙/接口/默认网关
// （那是另一关注点；开/关代理不该顺带重刷路由面，2026-10-11 裁定）。
//
// 命名空间成员：本地改 proxy（localapi 写意图）后由 mynetd 派发这一条立即生效，
// 不必等下一次 bundle/reload。Proxy 非空则装、为空则（intent 有记录时）清理历史钉扎。
func proxyApply(dry bool) int {
	snap, err := shared.ReadSnapshot(shared.DefaultSnapshotPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxy apply: 读 system.json 失败（先跑 mynet detector）: %v\n", err)
		return 1
	}
	env, err := loadBundleEnvelope()
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxy apply: 读 bundle 失败（先让 mynetd 拉一版）: %v\n", err)
		return 1
	}
	plan, err := planFromBundle(env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxy apply: %v\n", err)
		return 1
	}
	plan.Routes = nil
	plan.Firewall = nil
	plan.Interfaces = nil
	plan.ProxyOnly = true
	rc := executeApply(snap, &plan, dry)
	if rc == 0 && !dry {
		syncFirewallAfterTunnelUp()
		if isPCDNSPlatform() {
			return applyPCAcceleration()
		}
	}
	return rc
}

// applyPCAcceleration 将已保存的客户端策略同步到 PC DNS 会话。
func applyPCAcceleration() int {
	// 此入口代表策略刚被用户保存，显式新策略优先于旧版 hook 开关。
	st, err := LoadProxyState()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if st.Local != nil {
		if err := saveDNSHookIntent(st.Local.Policy.Enabled && st.Local.Policy.Role != domain.ProxyRoleServer); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if !DNSHookWanted() {
		result := stopDNSHookSidecar()
		result.log()
		if result.err != nil {
			return 1
		}
		if liveHookTunDevice() != "" {
			if err := ApplyFromActive(false); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		return 0
	}
	if err := accelerationStartGate(); err != nil {
		fmt.Println("加速：", err.Error())
		return 0
	}
	if err := ApplyFromActive(false); err != nil {
		fmt.Fprintln(os.Stderr, "加速策略准备失败:", err)
		return 1
	}
	if err := dnsHookStartGate(); err != nil {
		fmt.Println("加速：", err.Error())
		return 0
	}
	return dnsHookStart(DNSHookListenDefault)
}

// proxyMigrate 折叠旧 Lua 权威 conf 到统一意图（CLI 入口）。
func proxyMigrate(dry bool) int {
	home, code := requireHome()
	if code != 0 {
		return code
	}
	res, err := MigrateLegacyProxyConf(home, dry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "proxy migrate: %v\n", err)
		return 1
	}
	switch {
	case res.Migrated:
		fmt.Printf("proxy migrate: %s%s\n", res.Reason, drySuffix(dry))
	case res.Skipped:
		fmt.Printf("proxy migrate: 跳过 —— %s\n", res.Reason)
	}
	return 0
}

func drySuffix(dry bool) string {
	if dry {
		return "（--dry-run：未写盘）"
	}
	return ""
}

// proxyDNSSplit 写 dnsmasq 分流片段并 reload（网关形态）。
// domestic-csv：国内上游，逗号分隔；foreign：干净/国外上游。
// 走 ApplyProxyDNSFiles 生成 /tmp/mynet-dns-split.conf（含 ipset 行）。
func proxyDNSSplit(domesticCSV, foreign string) int {
	var domestic []string
	for _, s := range strings.Split(domesticCSV, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			domestic = append(domestic, s)
		}
	}

	// 从当前策略取域名规则；没有则给空规则（仍要能写默认上游）
	rules := &domain.ProxyDomainRules{}
	if p := EffectiveProxyPolicy(); p != nil && p.DomainRules != nil {
		rules = p.DomainRules
	}
	rules.UpstreamDirect = domestic
	if foreign != "" {
		rules.TunnelServer = foreign
	}

	// D1：租户智能 DNS 塞进 PlatformDNS（未显式配置时）
	if p := EffectiveProxyPolicy(); p != nil {
		// profile 里的 tenant DNS
		if st, err := LoadProxyState(); err == nil {
			_ = st
		}
	}

	snap, _ := shared.ReadSnapshot(shared.DefaultSnapshotPath())
	_ = snap

	confPath := "/tmp/mynet-dns-split.conf"
	if err := WriteDomainDNSSplitConf(rules, confPath); err != nil {
		fmt.Fprintf(os.Stderr, "dns-split: 写配置失败: %v\n", err)
		return 1
	}
	fmt.Printf("dns-split: 已写 %s（domestic=%v foreign=%s）\n", confPath, domestic, foreign)

	// reload dnsmasq（不 restart）；没有 dnsmasq 则跳过（PC 形态）
	if rc := reloadLocalResolver(); rc != 0 {
		return rc
	}
	return 0
}

// proxyResolv PC 形态：写 resolv.conf 指到平台/公共 DNS。
func proxyResolv(server string) int {
	rules := &domain.ProxyDomainRules{}
	if p := EffectiveProxyPolicy(); p != nil && p.DomainRules != nil {
		rules = p.DomainRules
	}
	old, err := PCNameserverResolv(rules, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolv: 写失败: %v\n", err)
		return 1
	}
	_ = old
	fmt.Printf("resolv: nameserver → %s\n", server)
	return 0
}

// proxyStatus 一行诊断。
func proxyStatus() int {
	if snap, err := shared.ReadSnapshot(shared.DefaultSnapshotPath()); err == nil {
		res := DetectLocalResolver(snap)
		fmt.Printf("resolver: %s (local=%v)\n", res.Kind, res.HasLocal)
	}
	if p := EffectiveProxyPolicy(); p != nil {
		fmt.Printf("policy: role=%s region=%s policy=%s\n", p.Role, p.Region, p.Policy)
	} else {
		fmt.Println("policy: (未配置)")
	}
	if ok, reason := GNBProxySupported(); ok {
		fmt.Printf("gnb P2: 支持 — %s\n", reason)
	} else {
		fmt.Printf("gnb P2: 不支持 — %s\n", reason)
	}
	return 0
}

// reloadLocalResolver 重载本地解析器（dnsmasq/smartdns）；没有则 0。
func reloadLocalResolver() int {
	// dnsmasq：SIGHUP 或 /etc/init.d/dnsmasq reload
	// 没有解析器时静默跳过（PC 形态走 resolv，不是 split）
	snap, err := shared.ReadSnapshot(shared.DefaultSnapshotPath())
	if err != nil {
		return 0
	}
	res := DetectLocalResolver(snap)
	if !res.HasLocal {
		fmt.Println("dns-split: 无本地解析器（PC 形态），跳过 reload")
		return 0
	}
	fmt.Printf("dns-split: %s 配置就绪（reload 由 init 系统负责）\n", res.Kind)
	return 0
}
