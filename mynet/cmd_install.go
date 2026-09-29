package mynet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// cmdInstall — **服务安装**（不是安装包安装）。
//
//	mynet install [--prefix DIR] [--home DIR] [--gateways URL[,URL]]
//	               [--gnb-bin PATH] [--no-enable] [--dry-run]
//
// 职责边界（全平台一致）：
//   - **文件落地**（mynet/mynetd/gnb 二进制、LuCI、默认单元）归**安装包**
//     （OpenWrt ipk · deb/rpm · macOS pkg · Windows MSI · dist 包解压）。
//   - **服务装配**归 `mynet install`：写 env、写服务单元、enable/start。
//
// 为什么不 copy 二进制：包管理器负责放置/校验/卸载；install 再 copy 一份会
// 和包打架（覆盖、双份、卸载残留）。缺二进制时明确报错，让人去装包。
//
// 历史坑：早期 install 只写二进制不写单元，报成功但服务不存在；后补全单元时
// 又把 copy 混进来。现在两条线分开：包放文件，install 装服务。
func cmdInstall(args []string) int {
	prefix := defaultPrefix()
	home := ""
	gateways := ""
	gnbBin := "/usr/local/bin/gnb"
	enable := true
	dry := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--prefix":
			if i+1 >= len(args) {
				return usageInstall("--prefix 需要目录")
			}
			i++
			prefix = args[i]
		case "--home":
			if i+1 >= len(args) {
				return usageInstall("--home 需要目录")
			}
			i++
			home = args[i]
		case "--gateways":
			if i+1 >= len(args) {
				return usageInstall("--gateways 需要 URL")
			}
			i++
			gateways = args[i]
		case "--gnb-bin":
			if i+1 >= len(args) {
				return usageInstall("--gnb-bin 需要路径")
			}
			i++
			gnbBin = args[i]
		case "--no-enable":
			enable = false
		case "--dry-run":
			dry = true
		default:
			return usageInstall("未知参数 " + args[i])
		}
	}
	if home == "" {
		home = prefix
	}
	if gateways == "" {
		// 节点模式必须知道 gateway 在哪，否则 mynetd 起来是「纯本机模式」
		// （无 WS、无节点身份、不碰驱动），装了等于没装。
		return usageInstall("需要 --gateways wss://…（节点模式的 WS 入口；不给则 mynetd 只有 localapi，隧道起不来）")
	}
	// Windows 走独立路径：没有 systemd/launchd，二进制名带 .exe（V5-2）
	if runtime.GOOS == "windows" {
		return installWindows(prefix, home, gateways, gnbBin, enable, dry)
	}
	// OpenWrt 走 procd（E-1）：写 /etc/init.d/{mynetd,mynet}，不用 systemd
	if isOpenWrt() {
		return installOpenWrt(prefix, home, gateways, gnbBin, enable, dry)
	}
	if err := assertUnitsEmbedded(); err != nil {
		fmt.Fprintln(os.Stderr, "内嵌单元异常：", err)
		return 1
	}

	binDir := filepath.Join(prefix, "bin")
	envPath := "/etc/mynet/mynetd.env"
	if runtime.GOOS == "darwin" {
		envPath = filepath.Join(prefix, "etc", "mynetd.env") // 仅作文档，launchd 用 EnvironmentVariables
	}

	fmt.Println("将执行以下动作（服务安装，--dry-run 只看不做）：")
	unitDir := "/etc/systemd/system"
	if runtime.GOOS == "darwin" {
		unitDir = launchdPlistDir
		fmt.Printf("  写 %s/%s.plist\n", unitDir, LabelMynetd)
		fmt.Println("  （gnb 不进 launchd：darwin 的 gnb 无 --systemd，由 mynetd 拉起）")
	} else {
		for _, u := range SystemdUnitNames {
			fmt.Printf("  写 %s/%s\n", unitDir, unitFileName(u))
		}
	}
	fmt.Printf("  检查二进制已在位（安装包负责）: %s/{mynet,mynetd}\n", binDir)
	fmt.Printf("  写 %s  (MYNET_HOME=%s, MYNET_GATEWAYS=%s, MYNET_BIN=%s/mynet)\n",
		envPath, home, gateways, binDir)
	fmt.Printf("  建 %s/{conf,var}\n", home)
	if enable {
		if runtime.GOOS == "darwin" {
			fmt.Println("  launchctl bootstrap system（com.mynet.mynetd；gnb 由 mynetd 拉起）")
		} else {
			fmt.Println("  systemctl daemon-reload")
			fmt.Println("  systemctl enable --now mynetd mynet_gnb")
		}
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
	if runtime.GOOS == "windows" {
		mynetPath += ".exe"
		mynetdPath += ".exe"
	}
	for _, bin := range []string{mynetPath, mynetdPath} {
		if !fileExists(bin) {
			fmt.Fprintf(os.Stderr, "✗ 缺二进制 %s\n", bin)
			fmt.Fprintln(os.Stderr, "  文件落地归安装包（deb/rpm/pkg/MSI/dist），mynet install 只装服务")
			return 1
		}
	}

	// 1) 先停服务（改 env/单元时旧进程不能占着文件）
	if runtime.GOOS == "darwin" {
		_, _ = launchctl("bootout", "system/"+LabelMynetd)
		_, _ = launchctl("bootout", "system/"+LabelGNB)
	} else if out, err := runSystemctl("stop", "mynetd", "mynet_gnb"); err != nil {
		fmt.Fprintf(os.Stderr, "  systemctl stop（首次安装可忽略）: %v %s\n", err, strings.TrimSpace(string(out)))
	}

	// 2) MYNET_HOME 目录
	for _, sub := range []string{"conf", "var"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	// 3) 环境文件 + 4) 单元（抽成 writeEnvFile / writeSystemdUnits 以便测试）
	if runtime.GOOS != "darwin" {
		if err := writeEnvFile(envPath, home, gateways, binDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("installed", envPath)
		if err := writeSystemdUnits(unitDir, gnbBin); err != nil {
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
	} else {
		// launchd：plist 自带 EnvironmentVariables，不需要单独的 env 文件
		if err := installLaunchd(home, gateways, binDir, gnbBin, enable); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	// 6) gnb 位置校验：单元里写死了绝对路径，位置不对就只在启动那一刻才炸
	if _, err := os.Stat(gnbBin); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ %s 不存在 —— mynet_gnb.service 会起不来。请把 gnb 装到该路径，"+
			"或用 --gnb-bin 指定后重装。\n", gnbBin)
	}

	fmt.Println("\n下一步（节点身份，install 不代劳）：")
	fmt.Printf("  1) sudo %s setup --vpn-type gnb --back-pub <back 的公钥 hex>\n", filepath.Join(binDir, "mynet"))
	fmt.Println("  2) 把 setup 打印出的 public_hex 用**用户 token** 上传到 back")
	fmt.Println("     PUT /api/v1/nodes/<id>/keys  {\"custom_public_key\":\"<hex>\",\"key_type\":\"gnbkey\"}")
	fmt.Println("  3) sudo systemctl restart mynetd   # 触发启动生命周期：拉 bundle → 渲染配置 → 装路由")
	return 0
}

func usageInstall(msg string) int {
	fmt.Fprintln(os.Stderr, "用法: mynet install --gateways wss://… [--prefix DIR] [--home DIR] [--gnb-bin PATH] [--no-enable] [--dry-run]")
	if msg != "" {
		fmt.Fprintln(os.Stderr, "     ", msg)
	}
	return 2
}

// cmdUninstall — 停服务并移除安装件。
//
//	mynet uninstall [--home DIR] [--keep-bin] [--dry-run]
//
// ⚠️ 不动 $home 下的**节点身份与数据**（conf/mynet.conf 含节点私钥、
// var/gnb/security/ 含密钥、var/bundle.json 是配置态）。那些是业务数据，
// 卸载程序不该替人决定丢掉；要清得显式删目录。
func cmdUninstall(args []string) int {
	home := ""
	keepBin := false
	dry := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--home":
			if i+1 >= len(args) {
				return usageInstall("--home 需要目录")
			}
			i++
			home = args[i]
		case "--keep-bin":
			keepBin = true
		case "--dry-run":
			dry = true
		default:
			return usageInstall("未知参数 " + args[i])
		}
	}
	if home == "" {
		home = defaultPrefix()
	}

	fmt.Println("将执行以下动作（--dry-run 只看不做）：")
	if runtime.GOOS == "darwin" {
		fmt.Println("  launchctl bootout system/com.mynet.{mynetd,gnb}")
		for _, label := range []string{LabelMynetd, LabelGNB} {
			fmt.Printf("  删 %s/%s.plist\n", launchdPlistDir, label)
		}
	} else {
		fmt.Println("  systemctl disable --now mynetd mynet_gnb")
		for _, u := range SystemdUnitNames {
			fmt.Printf("  删 /etc/systemd/system/%s\n", unitFileName(u))
		}
		fmt.Println("  删 /etc/mynet/mynetd.env")
	}
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

	if runtime.GOOS == "darwin" {
		if err := uninstallLaunchd(keepBin, home); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ uninstall launchd: %v（继续删文件）\n", err)
		}
	} else {
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
	}
	if !keepBin {
		for _, b := range []string{"mynet", "mynetd"} {
			p := filepath.Join(home, "bin", b)
			if err := os.Remove(p); err == nil {
				fmt.Println("removed", p)
			}
		}
	}
	fmt.Printf("\n已卸载。节点身份与密钥仍保留在 %s —— 如需彻底清除请自行 rm 该目录（含节点私钥）。\n", home)

	return 0
}

// defaultPrefix 见 prefix_{unix,windows}.go。

// fileExists 存在性判断（比到处写 os.Stat 两次可读）。
func fileExists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}

// runSystemctl 调 systemd。driver 包的 execCombined 在另一个包里、未导出，
// 这里不为一个 shell 调用去改它的可见性（那会把一个纯执行器 helper
// 变成跨包 API）。
func runSystemctl(args ...string) ([]byte, error) {
	return exec.Command("systemctl", args...).CombinedOutput()
}

// writeEnvFile 写 /etc/mynet/mynetd.env。0600 —— 它指向的 MYNET_HOME 里含节点私钥。
func writeEnvFile(path, home, gateways, binDir string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := "# 由 mynet install 生成。改配置请改这里，不要改单元\n" +
		"MYNET_HOME=" + home + "\n" +
		"MYNET_GATEWAYS=" + gateways + "\n" +
		"MYNET_BIN=" + filepath.Join(binDir, "mynet") + "\n"

	return os.WriteFile(path, []byte(body), 0o600)
}

// writeSystemdUnits 把内嵌的单元写到 unitDir（生产是 /etc/systemd/system）。
//
// ⚠️ gnb 的路径是写死的绝对路径：systemd 的 ExecStart **第一个 token 必须是绝对路径**，
// 不能是 ${VAR}。所以装的 gnb 不在默认位置时要替换进单元里 —— 否则单元装得上、
// 起不来，而错误只在启动那一刻才出现。
func writeSystemdUnits(unitDir, gnbBin string) error {
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	for _, u := range SystemdUnitNames {
		body, _ := systemdUnit(u)
		if u == "mynet_gnb" {
			body = strings.ReplaceAll(body, "/usr/local/bin/gnb", gnbBin)
		}
		dst := filepath.Join(unitDir, unitFileName(u))
		if err := os.WriteFile(dst, []byte(body), 0o644); err != nil {
			return err
		}
	}

	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	return os.WriteFile(dst, b, perm)
}
