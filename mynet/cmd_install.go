package mynet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// cmdInstall — 把 mynet 装成系统服务。
//
//	mynet install [--prefix DIR] [--home DIR] [--gateways URL[,URL]]
//	               [--gnb-bin PATH] [--no-enable] [--dry-run]
//
// ⚠️ 此前这里只复制二进制，然后打印一句
//
//	service unit: /etc/systemd/system/mynetd.service
//	enable: 请按平台执行 systemctl/launchctl
//
// 就返回 0 —— 单元文件本身**没有被安装**。所以一台新机器上只拿到二进制时，
// `mynet install` 装完仍是「没有服务、重启就没了」，而它还报成功。
// 我在两台验证机上装服务时那一步是**手工做的**（cp 单元 + daemon-reload +
// enable --now），等于把 install 该干的活自己干了一遍 —— 这个洞就是那时发现的。
//
// 现在它做完整条链：写二进制 → 建 $home → 写 /etc/mynet/mynetd.env →
// 写单元（内嵌在二进制里，见 units.go）→ daemon-reload → enable --now。
// 需要 root（要写 /etc/systemd/system 与 /etc/mynet）。
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
	if err := assertUnitsEmbedded(); err != nil {
		fmt.Fprintln(os.Stderr, "内嵌单元异常：", err)
		return 1
	}

	fmt.Println("将执行以下动作（--dry-run 只看不做）：")
	binDir := filepath.Join(prefix, "bin")
	envPath := "/etc/mynet/mynetd.env"
	unitDir := "/etc/systemd/system"
	for _, u := range SystemdUnitNames {
		fmt.Printf("  写 %s/%s\n", unitDir, unitFileName(u))
	}
	fmt.Printf("  写 %s  (MYNET_HOME=%s, MYNET_GATEWAYS=%s, MYNET_BIN=%s/mynet)\n",
		envPath, home, gateways, binDir)
	fmt.Printf("  建 %s/{conf,var}\n", home)
	fmt.Printf("  装二进制到 %s/{mynet,mynetd}\n", binDir)
	if enable {
		fmt.Println("  systemctl daemon-reload")
		fmt.Println("  systemctl enable --now mynetd mynet_gnb")
	}
	if runtime.GOOS != "linux" {
		fmt.Fprintf(os.Stderr, "  ⚠️ 本命令目前只支持 Linux（systemd）；当前是 %s，下面的单元安装步骤会失败\n", runtime.GOOS)
	}
	if dry {
		fmt.Println("\n--dry-run：以上都没有执行")
		return 0
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "install 需要 root：要写 /etc/systemd/system 与 /etc/mynet。请 sudo mynet install …")
		return 1
	}

	// 1) 二进制
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	self, _ := os.Executable()
	if err := copyFile(self, filepath.Join(binDir, "mynet"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "install mynet: %v\n", err)
		return 1
	}
	if src := filepath.Join(filepath.Dir(self), "mynetd"); fileExists(src) {
		if err := copyFile(src, filepath.Join(binDir, "mynetd"), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "install mynetd: %v\n", err)
			return 1
		}
	} else {
		fmt.Fprintf(os.Stderr, "⚠️ 找不到同目录的 mynetd（%s），只装了 mynet —— 节点守护进程会缺失\n", src)
	}

	// 2) MYNET_HOME 目录
	for _, sub := range []string{"conf", "var"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	// 3) 环境文件 + 4) 单元（抽成 writeEnvFile / writeSystemdUnits 以便测试）
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

func defaultPrefix() string {
	if runtime.GOOS == "darwin" {
		return "/usr/local/opt/mynet"
	}

	return "/opt/mynet"
}

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
