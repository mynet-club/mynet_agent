//go:build linux && !openwrt

package mynet

// 通用 Linux **执行面** —— 上层合同（规则集 + 产物形状 + 持久化面选择）在
// firewall_persist_common.go，本文件只负责「在这台 Linux 上把合同落地」。
//
// 与 OpenWrt 的关系：产物路径、脚本形状、入口函数**完全一致**（`mynet firewall
// sync`、`apply --auto` 尾部、`service start` 隧道就绪后都走同一函数），差别只在
// 「谁来在防火墙重启后调用它」：
//
//	OpenWrt  fw4 的设计里就有 include 段 → 登记 UCI mynet_include
//	Linux    没有这种钩子 → 按探测到的持久化面登记（见 selectFirewallBackend）
//
// 本机实测（2026-10-16，Ubuntu 22.04 / iptables-nft）佐证了为什么不能想当然：
// `/etc/nftables.conf` 存在但**不 include** `/etc/nftables.d/`，`/etc/iptables/rules.v4`
// 存在但 `netfilter-persistent` 根本没装 → 两条路都是「看着有、实际没人重放」。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/logger"
)

// WriteFirewallInclude 落地上层合同：
//  1. 写 $MYNET_HOME/scripts/firewall.mynet.include（与 OpenWrt 同一产物）
//  2. 按探测到的后端登记持久化面
//
// tunDev 可为空（bundle 未就绪）：产物只出骨架，等 sync 按实际接口名重写。
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
	// nft 产物是**系统原生加载器** `nft -f` 的输入（规则本体不进 Go，见 LoadFirewallRules）。
	if err := WriteFirewallNftArtifact(home, tunDev); err != nil {
		return fmt.Errorf("写 nft 产物: %w", err)
	}

	switch backend := currentFirewallBackend(); backend {
	case FirewallBackendNftablesD:
		return writeNftDropIn(tunDev)
	case FirewallBackendIPtablesPersistent:
		// 运行期规则已由 apply 装进内核，这里只是把它们存下来。
		if out, err := exec.Command("netfilter-persistent", "save").CombinedOutput(); err != nil {
			return fmt.Errorf("netfilter-persistent save: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	case FirewallBackendNone:
		// 丢的只是「防火墙重启后自动重放」，**产物与当次加载照做**：
		// 返回哨兵而非硬失败，调用方告警后仍继续（见 ErrNoPersistenceSurface 注释）。
		return ErrNoPersistenceSurface
	default:
		return fmt.Errorf("防火墙由 %s 管理，mynet 尚未接入它的持久化面（规则只在运行期有效）", backend)
	}
}

// writeNftDropIn 写 nftables 加载面里的 mynet 自有文件（不动用户文件）。
func writeNftDropIn(tunDev string) error {
	content, err := FirewallNftRules(tunDev)
	if err != nil {
		return err
	}
	path := FirewallNftDropInPath()
	if content == "" {
		// 无规则（tun 未就绪）：清掉上一版，免得留着旧接口名的规则。
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清 nft drop-in: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("建 %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写 nft drop-in: %w", err)
	}
	return nil
}

// LoadFirewallRules 委托系统原生加载器：把**每一个 nft 产物**交给 `nft -f`。
// Go 不 exec `nft add rule` —— 规则本体全部在文件里（网关 + proxy 各一份）。
func LoadFirewallRules(home string) error {
	return LoadNftArtifacts(home)
}

// SyncFirewallTunDevice Linux 无 UCI。等价物 = 让落盘的持久化产物跟上**当前** tun 名
// （tun 名变了而产物还写旧名，症状是重启后规则匹配不到接口 = 静默失效）。
// 返回 changed。
func SyncFirewallTunDevice(tunDev string) (bool, error) {
	if err := validateTunDeviceName(tunDev); err != nil {
		return false, err
	}
	if tunDev == "" {
		return false, nil
	}
	if currentFirewallBackend() != FirewallBackendNftablesD {
		// 其余后端的产物是「运行期规则集的快照」，无接口名漂移问题。
		return false, nil
	}
	path := FirewallNftDropInPath()
	want, err := FirewallNftRules(tunDev)
	if err != nil {
		return false, err
	}
	cur, err := os.ReadFile(path)
	if err == nil && string(cur) == want {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("读 %s: %w", path, err)
	}
	if err := writeNftDropIn(tunDev); err != nil {
		return false, err
	}
	// 已加载的旧规则要换掉：nftables.service reload 才会重读 drop-in。
	// 没这个 unit 时（纯手写 nft 的机器）只告警不阻断 —— 文件已经对了。
	if out, err := exec.Command("systemctl", "reload", "nftables").CombinedOutput(); err != nil {
		logger.Comp("apply").Warn("nftables reload 失败（drop-in 已更新，重启后生效）",
			"err", err, "out", strings.TrimSpace(string(out)))
	}
	return true, nil
}

// currentFirewallBackend 本机持久化面（执行面填样本 + 纯函数选择）。
func currentFirewallBackend() FirewallBackend {
	return selectFirewallBackend(probeFirewallBackendFacts())
}

// probeFirewallBackendFacts 采样本机事实（纯选择逻辑在 selectFirewallBackend）。
func probeFirewallBackendFacts() FirewallBackendFacts {
	return FirewallBackendFacts{
		FirewalldActive:       systemdUnitActive("firewalld"),
		UfwActive:             ufwActive(),
		NftablesUnitPresent:   systemdUnitLoaded("nftables"),
		NftablesConfIncludesD: nftablesConfIncludesDropInDir(),
		NetfilterPersistent:   commandExists("netfilter-persistent"),
	}
}

func systemdUnitLoaded(unit string) bool {
	out, err := exec.Command("systemctl", "list-unit-files", unit+".service", "--no-legend").Output()
	return err == nil && strings.Contains(string(out), unit+".service")
}

func systemdUnitActive(unit string) bool {
	if !systemdUnitLoaded(unit) {
		return false
	}
	out, err := exec.Command("systemctl", "is-active", unit).Output()
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

// ufwActive 看 ufw 自己报的状态，不看包是否装了（装了但 inactive 等于没在管）。
func ufwActive() bool {
	if !commandExists("ufw") {
		return false
	}
	out, err := exec.Command("ufw", "status").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Status: active")
}

// nftablesConfIncludesDropInDir 判定 /etc/nftables.d/ 是否真的被加载。
// 只看目录存在是不够的 —— 没人 include 时写进去等于没写（本机实测如此）。
func nftablesConfIncludesDropInDir() bool {
	b, err := os.ReadFile("/etc/nftables.conf")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "include") {
			continue
		}
		if strings.Contains(line, "/etc/nftables.d/") {
			return true
		}
	}
	return false
}
