//go:build linux && openwrt

package mynet

// OpenWrt **执行面** —— 上层合同（规则集 + 产物形状）在 firewall_persist_common.go，
// 本文件只负责「在 OpenWrt 上把合同落地」（2026-10-12 用户裁定 + 2026-10-16 分层）：
//
//  1. `$MYNET_HOME/scripts/firewall.mynet.include` **必须保留** —— fw4 reload/
//     restart 会清掉 `nft add` 手写的 NAT 规则，这个 include 脚本在 fw4 起来后
//     被调用重放；丢了它 =「防火墙一重启规则就丢」。产物内容由上层渲染（幂等），
//     UCI `config include 'mynet_include'` 指向它。
//  2. network.mynet / zone mynet 的 device 必须跟随**当前** tun 名（bundle
//     node.interface，唯一解析口 domain.BundleEnvelope.TunDevice）。绑定写死旧名
//     的实证（.178，2026-10-12）：fw4 只放行旧接口名，LAN→隧道 SYN 被 forward
//     兜底 reject 秒回 RST，代理流量全灭。
//
// 入口：`mynet firewall sync`（apply --auto 尾部、service start 隧道就绪后、
// install 也走同一函数）。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// currentFirewallBackend OpenWrt 的持久化面就是 fw4 的 include 段
// （fw4 reload/restart 后会调用 include 脚本重放规则）。
func currentFirewallBackend() FirewallBackend { return FirewallBackendFw4 }

// WriteFirewallInclude 写 include 脚本（0755）并保证 UCI include 段指向它。幂等。
// tunDev 可为空（bundle 未就绪）：脚本只出骨架，等 sync 按实际接口名重写。
func WriteFirewallInclude(home, tunDev string) error {
	if err := ensureProxyExitReplay(home, tunDev); err != nil {
		return err
	}
	body, err := FirewallIncludeScript(tunDev)
	if err != nil {
		return err
	}
	path := FirewallIncludePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("建 scripts 目录: %w", err)
	}
	if err := writeFirewallArtifact(path, []byte(body), 0o755); err != nil {
		return fmt.Errorf("写 include: %w", err)
	}
	// nft 产物是 include 末尾 `for f in *.nft` 的输入 —— OpenWrt 也必须写，
	// 否则 fw4 reload 时只有 include 里的网关规则，proxy 集合/规则全丢。
	if err := WriteFirewallNftArtifact(home, tunDev); err != nil {
		return fmt.Errorf("写 nft 产物: %w", err)
	}
	// UCI 登记：命名段 `config include 'mynet_include'`（type script）。
	// 段名即标识 —— fw4 的 include 段**不认 name option**（写 name 会报
	// unknown option 并被忽略，2026-10-12 .178 实证），别加。
	// 顺手清掉旧写法留下的匿名 @include 段（两代同名即旧的一代）。
	step := `set -e; for x in $(uci show firewall | awk -F"[.=]" '/^firewall\..*=include$/ {print $2}'); do ` +
		`[ "$x" = "mynet_include" ] && continue; ` +
		`[ "$(uci -q get firewall.$x.path)" = "` + path + `" ] && uci -q delete firewall.$x; ` +
		`done; ` +
		`uci -q get firewall.mynet_include >/dev/null || uci set firewall.mynet_include=include; ` +
		`uci set firewall.mynet_include.enabled=1; ` +
		`uci set firewall.mynet_include.type=script; ` +
		`uci set firewall.mynet_include.fw4_compatible=1; ` +
		`uci set firewall.mynet_include.path=` + path + `; uci commit firewall`
	if out, err := exec.Command("sh", "-c", step).CombinedOutput(); err != nil {
		return fmt.Errorf("uci include 登记: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// LoadFirewallRules 委托系统加载：跑**同一份 include 脚本** —— fw4 的加载机制
// 本来就是「执行 include 段指向的脚本」，我们跑的是系统要跑的那份产物，
// 不是自己拼 nft 语句（裁定：规则本体不进 Go）。
func LoadFirewallRules(home string) error {
	path := FirewallIncludePath(home)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("include 产物不存在: %w", err)
	}
	if out, err := exec.Command("sh", path).CombinedOutput(); err != nil {
		return fmt.Errorf("加载 include: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SyncFirewallTunDevice 把 network.mynet.device 与 zone mynet 的 device 对齐
// tunDev（幂等：一致不动、不一致才写+firewall reload）。返回 changed。
func SyncFirewallTunDevice(tunDev string) (bool, error) {
	if err := validateTunDeviceName(tunDev); err != nil {
		return false, err
	}
	if tunDev == "" {
		return false, nil
	}
	p := EffectiveProxyPolicy()
	wanEnabled := p != nil && p.Enabled && p.IsServerLike()
	step := `set -e; changed=0; ` +
		`[ "$(uci get network.mynet.device 2>/dev/null)" = "` + tunDev + `" ] || { uci set network.mynet.device=` + tunDev + `; uci commit network; changed=1; }; ` +
		openWrtZoneLookup +
		`[ "$(uci get firewall.$s.device 2>/dev/null)" = "` + tunDev + `" ] || { uci set firewall.$s.device=` + tunDev + `; uci commit firewall; changed=1; }; ` +
		openWrtWANForwardingStep(wanEnabled) + `echo $changed`
	out, err := exec.Command("sh", "-c", step).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("uci device 对齐: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	changed := strings.TrimSpace(string(out)) == "1"
	if changed {
		// fw4 重建放行链（oifname 匹配新名）；失败要指因 —— 不 reload 则
		// 「改了配置但还在 reject」，比不改更难查。
		if out, err := exec.Command("/etc/init.d/firewall", "reload").CombinedOutput(); err != nil {
			return true, fmt.Errorf("device 已对齐 %s，但 firewall reload 失败: %w (%s)",
				tunDev, err, strings.TrimSpace(string(out)))
		}
	}
	return changed, nil
}

// WAN forwarding is plumbing for an active grant; the nft WAN guard remains
// authoritative even if a third-party restores an old broad forwarding entry.
func openWrtWANForwardingStep(enabled bool) string {
	if enabled {
		return `f(){ ` + uciLoopForwarding + `[ "$(uci -q get firewall.$x.src)" = mynet ] && [ "$(uci -q get firewall.$x.dest)" = wan ] && return 0; done; return 1; }; f || { s=$(uci add firewall forwarding); uci set firewall.$s.src=mynet; uci set firewall.$s.dest=wan; changed=1; }; ` + `[ "$changed" = 0 ] || uci commit firewall; `
	}
	return uciLoopForwarding + `[ "$(uci -q get firewall.$x.src)" = mynet ] && [ "$(uci -q get firewall.$x.dest)" = wan ] || continue; uci delete firewall.$x; changed=1; done; [ "$changed" = 0 ] || uci commit firewall; `
}

func firewalldEventHookActive() bool { return false }
