//go:build linux && openwrt

package mynet

// OpenWrt 防火墙持久化与接口绑定同步（2026-10-12 用户裁定）：
//
//  1. `$MYNET_HOME/scripts/firewall.mynet.include` **必须保留** —— fw4 reload/
//     restart 会清掉 `nft add` 手写的 NAT 规则，这个 include 脚本在 fw4 起来后
//     被调用重放；丢了它 =「防火墙一重启规则就丢」。由本文件生成（幂等），
//     UCI `config include 'mynet_include'` 指向它。
//  2. network.mynet / zone mynet 的 device 必须跟随**当前** tun 名（bundle
//     node.interface，唯一解析口 domain.BundleEnvelope.TunDevice）。绑定写死旧名
//     的实证（.178，2026-10-12）：fw4 只放行旧接口名，LAN→隧道 SYN 被 forward
//     兜底 reject 秒回 RST，代理流量全灭。
//
// 入口：`mynet firewall sync`（apply --auto 尾部与 install 也走同一函数）。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WriteFirewallInclude 写 include 脚本（0755）并保证 UCI include 段指向它。幂等。
// tunDev 可为空（bundle 未就绪）：脚本只出骨架，等 sync 按实际接口名重写。
func WriteFirewallInclude(home, tunDev string) error {
	body, err := FirewallIncludeScript(tunDev)
	if err != nil {
		return err
	}
	path := FirewallIncludePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("建 scripts 目录: %w", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		return fmt.Errorf("写 include: %w", err)
	}
	// UCI 登记：config include 'mynet_include'（type script）。
	// section 名统一叫 mynet_include —— 与旧一代同名同址，两代同名即旧的一代被顶掉。
	step := `s=$(uci show firewall | awk -F"[.=]" '/^firewall\..*\.name=.mynet_include.$/ {print $2; exit}'); ` +
		`[ -n "$s" ] || s=$(uci add firewall include); ` +
		`uci set firewall.$s.name=mynet_include; uci set firewall.$s.type=script; ` +
		`uci set firewall.$s.path=` + path + `; uci commit firewall`
	if out, err := exec.Command("sh", "-c", step).CombinedOutput(); err != nil {
		return fmt.Errorf("uci include 登记: %w (%s)", err, strings.TrimSpace(string(out)))
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
	step := `changed=0; ` +
		`[ "$(uci get network.mynet.device 2>/dev/null)" = "` + tunDev + `" ] || { uci set network.mynet.device=` + tunDev + `; uci commit network; changed=1; }; ` +
		openWrtZoneLookup +
		`[ "$(uci get firewall.$s.device 2>/dev/null)" = "` + tunDev + `" ] || { uci set firewall.$s.device=` + tunDev + `; uci commit firewall; changed=1; }; ` +
		`echo $changed`
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
