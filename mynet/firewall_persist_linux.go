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
)

// WriteFirewallInclude 落地上层合同：
//  1. 写 $MYNET_HOME/scripts/firewall.mynet.include（与 OpenWrt 同一产物）
//  2. 按探测到的后端登记持久化面
//
// tunDev 可为空（bundle 未就绪）：产物只出骨架，等 sync 按实际接口名重写。
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
	// nft 产物是**系统原生加载器** `nft -f` 的输入（规则本体不进 Go，见 LoadFirewallRules）。
	if err := WriteFirewallNftArtifact(home, tunDev); err != nil {
		return fmt.Errorf("写 nft 产物: %w", err)
	}

	switch backend := currentFirewallBackend(); backend {
	case FirewallBackendNftablesD:
		if nftablesConfIncludesDropInDir() {
			return writeNftDropIn(home)
		}
		return installFirewallServiceHook(home, "nftables")
	case FirewallBackendUfw:
		return installUfwReplayHook(home, "/etc/ufw/after.init")
	case FirewallBackendFirewalld:
		return installFirewalldReloadHooks(home)
	case FirewallBackendIPtablesPersistent:
		// iptables-save 不会保存 nft 自有表；随系统加载器启动重放同一产物。
		return writeNetfilterReplayPlugin(home)
	case FirewallBackendNone:
		// 丢的只是「防火墙重启后自动重放」，**产物与当次加载照做**：
		// 返回哨兵而非硬失败，调用方告警后仍继续（见 ErrNoPersistenceSurface 注释）。
		return ErrNoPersistenceSurface
	default:
		return fmt.Errorf("防火墙由 %s 管理，mynet 尚未接入它的持久化面（规则只在运行期有效）", backend)
	}
}

// writeNftDropIn 写 nftables 加载面里的 mynet 自有文件（不动用户文件）。
func writeNftDropIn(home string) error {
	content := firewallNftDropIn(home)
	path := FirewallNftDropInPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("建 %s: %w", filepath.Dir(path), err)
	}
	if err := writeFirewallArtifact(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写 nft drop-in: %w", err)
	}
	return nil
}

// LoadFirewallRules 委托系统原生加载器：把**每一个 nft 产物**交给 `nft -f`。
// Go 不 exec `nft add rule` —— 规则本体全部在文件里（网关 + proxy 各一份）。
func LoadFirewallRules(home string) error {
	return LoadNftArtifacts(home)
}

// SyncFirewallTunDevice Linux 无 UCI；接口由当次产物写入与加载完成同步。
func SyncFirewallTunDevice(tunDev string) (bool, error) {
	// WriteFirewallInclude 已写当前接口产物，LoadFirewallRules 已加载它。
	// 不重载整套系统防火墙，避免再次 flush 其他刚应用的规则。
	return false, validateTunDeviceName(tunDev)
}

func writeNetfilterReplayPlugin(home string) error {
	path := NetfilterReplayPluginPath
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return writeFirewallArtifact(path, []byte(netfilterReplayPlugin(home)), 0755)
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
	return nftablesConfLoadsDropIn(string(b))
}

func removeLinuxFirewallRegistration(home string) error {
	for path, want := range map[string]string{FirewallNftDropInPath(): firewallNftDropIn(home), NetfilterReplayPluginPath: netfilterReplayPlugin(home)} {
		body, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if string(body) != want {
			continue
		} // 管理员或其他实例已接管时不删。
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	if err := removeUfwReplayHook(home, "/etc/ufw/after.init"); err != nil {
		return err
	}
	for _, service := range []string{"nftables", "firewalld"} {
		path := filepath.Join("/etc/systemd/system", service+".service.d", "90-mynet-replay.conf")
		body, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if string(body) == firewallServiceHook(home) {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	watchPath := "/etc/systemd/system/mynet-firewall-watch.service"
	body, err := os.ReadFile(watchPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if string(body) == firewalldWatchUnit(home, bin) {
		if out, err := exec.Command("systemctl", "disable", "--now", "mynet-firewall-watch.service").CombinedOutput(); err != nil {
			return fmt.Errorf("stop firewalld event hook: %w: %s", err, out)
		}
		if err := os.Remove(watchPath); err != nil {
			return err
		}
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("unregister firewall hooks: %w: %s", err, out)
	}
	return nil
}

func installFirewallServiceHook(home, service string) error {
	path := filepath.Join("/etc/systemd/system", service+".service.d", "90-mynet-replay.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	want := firewallServiceHook(home)
	old, _ := os.ReadFile(path)
	if string(old) == want {
		return nil
	}
	if err := writeFirewallArtifact(path, []byte(want), 0644); err != nil {
		return err
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("register firewall hook: %w: %s", err, out)
	}
	return nil
}
func installFirewalldReloadHooks(home string) error {
	if err := installFirewallServiceHook(home, "firewalld"); err != nil {
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	path := "/etc/systemd/system/mynet-firewall-watch.service"
	want := firewalldWatchUnit(home, bin)
	old, _ := os.ReadFile(path)
	if string(old) != want {
		if err := writeFirewallArtifact(path, []byte(want), 0644); err != nil {
			return err
		}
		if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
			return fmt.Errorf("register firewalld events: %w: %s", err, out)
		}
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "mynet-firewall-watch.service").CombinedOutput(); err != nil {
		return fmt.Errorf("start firewalld events: %w: %s", err, out)
	}
	return nil
}

func firewalldEventHookActive() bool { return systemdUnitActive("mynet-firewall-watch") }
