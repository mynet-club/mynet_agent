//go:build linux && !openwrt

package mynet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Linux（systemd）服务装配后端 —— 编译期只进 `linux && !openwrt`（playbook §4.1）。
//
// 与 OpenWrt **不共享装配路径**：目录（/var/lib/mynet vs /etc/mynet、/opt/mynet/bin vs /sbin）、
// 服务（systemd vs procd）、防火墙（nft/iptables vs fw4/UCI zone）都不同。故本产物**不含**
// OpenWrt 后端；运行期探测到 OpenWrt 时明确拒绝并给指引，而不是夹带另一套逻辑。

// platformInstall 是 `mynet install` 的通用 Linux 入口（cmd_install.go 调用）。
func platformInstall(prefix, home, gateways, gnbBin string, enable, dry bool) int {
	if isOpenWrt() {
		return refuseOpenWrt()
	}

	return installSystemd(prefix, home, gateways, gnbBin, enable, dry)
}

// platformUninstall 是 `mynet uninstall` 的通用 Linux 入口。
// 在 OpenWrt 上同样拒绝：本产物从未在那装配过，也没有它的登记可清。
func platformUninstall(home string, keepBin, dry bool) int {
	if isOpenWrt() {
		return refuseOpenWrt()
	}

	return uninstallSystemd(home, keepBin, dry)
}

// refuseOpenWrt 通用 Linux 包在 OpenWrt 上的统一处置：
// 明确拒绝写入并输出修复指引（playbook §4.2「未知安装模式先拒绝写入」）。
func refuseOpenWrt() int {
	fmt.Fprintln(os.Stderr, "✗ 检测到 OpenWrt，但本二进制是**通用 Linux 包**，不含 procd/UCI 后端。")
	fmt.Fprintln(os.Stderr, "  OpenWrt 请用专用包：opkg install mynet-agent（或用 -tags openwrt 构建的 mynet/mynetd）。")
	fmt.Fprintln(os.Stderr, "  本包不会在 OpenWrt 上写 systemd 单元或 /etc/mynet —— 拒绝继续，避免半装配。")
	return 1
}

// platformDescriptor 本产物的平台事实。GOOS=linux 不足以表达发行平台 ——
// 运行期探测到 OpenWrt 时如实标注为「需专用包」。
func platformDescriptor() PlatformDescriptor {
	d := PlatformDescriptor{
		Platform:     "linux",
		PkgManager:   "apt/rpm",
		ServiceMgr:   "systemd",
		Capabilities: []string{"service.systemd", "apply.osroute", "apply.firewall"},
	}
	if isOpenWrt() {
		d.Platform = "openwrt"
		d.PkgManager = "opkg"
		d.ServiceMgr = "procd"
		d.Capabilities = []string{"install.unsupported:need-openwrt-package"}
	}

	return d
}

func installSystemd(prefix, home, gateways, gnbBin string, enable, dry bool) int {
	if err := assertSystemdUnitsEmbedded(); err != nil {
		fmt.Fprintln(os.Stderr, "内嵌单元异常：", err)
		return 1
	}

	binDir := filepath.Join(prefix, "bin")
	unitDir := "/etc/systemd/system"

	printInstallActionsHeader("服务安装", dry)
	for _, u := range SystemdUnitNames {
		fmt.Printf("  写 %s/%s\n", unitDir, unitFileName(u))
	}
	fmt.Printf("  检查二进制已在位（安装包负责）: %s/{mynet,mynetd}\n", binDir)
	fmt.Printf("  置备 gnb 四件套到标准位 %s/bin（源: 旧 home 树/包内位 → /usr/local/bin → --gnb-bin 同目录）\n", prefix)
	fmt.Printf("  写 %s/conf/gateways.json（网关列表）+ 把 MYNET_HOME=%s 写进 unit（不再有 env 文件）\n",
		home, home)
	fmt.Printf("  建 %s/{conf,var}\n", home)
	if enable {
		fmt.Println("  systemctl daemon-reload")
		fmt.Println("  systemctl enable --now mynetd mynet_gnb")
	}
	if dry {
		fmt.Println("\n--dry-run：以上都没有执行")
		return 0
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "install 需要 root：要写服务单元与 /etc/mynet。请 sudo mynet install …")
		fmt.Fprintln(os.Stderr, "  （TUN / 路由表 / 系统服务都属于系统层，桌面托盘本身不需要 root）")
		return 1
	}

	// 端口闸门：8096 被别人占着就不装；是旧版 MyNet 就走升级（保 conf）
	if err := mustAcceptPort(DefaultLocalAPI); err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return 1
	}

	// 0) 二进制必须已由安装包放好。缺了就去装包，不在这里 copy。
	mynetPath := filepath.Join(binDir, "mynet")
	mynetdPath := filepath.Join(binDir, "mynetd")
	for _, bin := range []string{mynetPath, mynetdPath} {
		if !fileExists(bin) {
			fmt.Fprintf(os.Stderr, "✗ 缺二进制 %s\n", bin)
			fmt.Fprintln(os.Stderr, "  文件落地归安装包（deb/rpm/pkg/MSI/dist），mynet install 只装服务")
			return 1
		}
	}

	// 0b) 清除「从互联网下载」标记 —— 不清 launchd/SCM 可能拒绝执行二进制
	for _, w := range clearDownloadMarks([]string{mynetPath, mynetdPath}) {
		fmt.Fprintln(os.Stderr, "⚠", w)
	}

	// 1) 先停服务（改 env/单元时旧进程不能占着文件）
	if out, err := runSystemctl("stop", "mynetd", "mynet_gnb"); err != nil {
		fmt.Fprintf(os.Stderr, "  systemctl stop（首次安装可忽略）: %v %s\n", err, strings.TrimSpace(string(out)))
	}

	// 2) MYNET_HOME 目录
	for _, sub := range []string{"conf", "var"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	// 2b) detector —— 装完文件的第一件事：写 var/system.json（L6 素材）。
	runInstallDetector(binDir, home)

	// 2c) 出厂补丁栈判定：编译期声明含 p2-route-proxy → 置备 proxy 插件包
	provisionProxyIfStamped(home)

	// 2d) gnb 四件套置备到「标准位」
	provisionDriverAndReport(prefix, home, gnbBin)

	// 3) 网关列表（conf/gateways.json）+ 4) 单元（MYNET_HOME 写进 unit，不再有 env 文件）
	if err := writeGatewaysFile(home, gateways); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("installed", filepath.Join(home, "conf", "gateways.json"))
	if err := writeSystemdUnits(unitDir, gnbBin, home); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, u := range SystemdUnitNames {
		fmt.Println("installed", filepath.Join(unitDir, unitFileName(u)))
	}

	// 5) 让 systemd 认识它们
	if _, err := runSystemctl("daemon-reload"); err != nil {
		fmt.Fprintf(os.Stderr, "systemctl daemon-reload: %v\n", err)
		return 1
	}
	if enable {
		if _, err := runSystemctl("enable", "--now", "mynetd", "mynet_gnb"); err != nil {
			fmt.Fprintf(os.Stderr, "systemctl enable --now: %v\n", err)
			return 1
		}
		fmt.Println("enabled + started: mynetd, mynet_gnb")
	}

	// 6) gnb 位置校验：单元里写死了绝对路径，位置不对就只在启动那一刻才炸
	warnGnbMissing(gnbBin, "mynet_gnb.service 会起不来。请把 gnb 装到该路径，或用 --gnb-bin 指定后重装。")

	printInstallNextSteps(binDir, false)
	return 0
}

func uninstallSystemd(home string, keepBin, dry bool) int {
	printInstallActionsHeader("", dry)
	fmt.Println("  systemctl disable --now mynetd mynet_gnb")
	for _, u := range SystemdUnitNames {
		fmt.Printf("  删 /etc/systemd/system/%s\n", unitFileName(u))
	}
	fmt.Println("  删 /etc/mynet/mynetd.env")
	if !keepBin {
		fmt.Printf("  删 %s/bin/{mynet,mynetd}\n", home)
	}
	fmt.Printf("  **保留** %s/{conf,var}（节点身份与密钥，卸载不替你丢业务数据）\n", home)
	if dry {
		fmt.Println("\n--dry-run：以上都没有执行")
		return 0
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "uninstall 需要 root。请 sudo mynet uninstall …")
		return 1
	}

	if _, err := runSystemctl("disable", "--now", "mynetd", "mynet_gnb"); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ systemctl disable --now: %v（继续删文件）\n", err)
	}
	for _, u := range SystemdUnitNames {
		p := filepath.Join("/etc/systemd/system", unitFileName(u))
		if err := os.Remove(p); err == nil {
			fmt.Println("removed", p)
		}
	}
	if err := os.Remove("/etc/mynet/mynetd.env"); err == nil {
		fmt.Println("removed /etc/mynet/mynetd.env")
	}
	if _, err := runSystemctl("daemon-reload"); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ systemctl daemon-reload: %v\n", err)
	}

	removeOwnBinaries(home, keepBin)
	uninstallTail(home)
	return 0
}

// writeSystemdUnits 把内嵌的单元写到 unitDir（生产是 /etc/systemd/system）。
//
// ⚠️ gnb 的路径是写死的绝对路径：systemd 的 ExecStart **第一个 token 必须是绝对路径**，
// 不能是 ${VAR}。所以装的 gnb 不在默认位置时要替换进单元里 —— 否则单元装得上、
// 起不来，而错误只在启动那一刻才出现。
//
// __GNB_CONF_REL__ 是 gnb -c 的 home 相对目标（D4-E：节点唯一存放位
// conf/<form>/<nodeId>，form+nodeId 由 install 时的 mode.json 决定）。
func writeSystemdUnits(unitDir, gnbBin, home string) error {
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	for _, u := range SystemdUnitNames {
		body, _ := systemdUnit(u)
		if u == "mynet_gnb" {
			body = strings.ReplaceAll(body, "/usr/local/bin/gnb", gnbBin)
			body = strings.ReplaceAll(body, "__GNB_CONF_REL__", gnbConfRel(home))
			// 缺口⑦：home 尚无 mode.json 时 gnbConfRel 回退 conf/<form>/default，
			// 单元天生指向错目录（188 实测 crash-loop 50 次）。单元照写
			// （install 的职责是装服务），但把「待归位」明说 ——
			// mynet setup 后由 realignGnbUnitAfterSetup 自动归位。
			if modeFileMissing(home) {
				warnGnbUnitConfRelPending(home)
			}
		}
		// MYNET_HOME 写进 unit（不再有 EnvironmentFile；见 spec/paths.md §2.1）
		body = strings.ReplaceAll(body, "__MYNET_HOME__", home)
		dst := filepath.Join(unitDir, unitFileName(u))
		if err := os.WriteFile(dst, []byte(body), 0o644); err != nil {
			return err
		}
	}

	return nil
}

// gnbConfRel 在 cmd_helpers.go（无 build-tag，迁移命令与 install 共用）。

// runSystemctl 调 systemd。
func runSystemctl(args ...string) ([]byte, error) {
	return exec.Command("systemctl", args...).CombinedOutput()
}
