//go:build windows

package mynet

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// installWindows — Windows **服务安装**（V5-2 / T5-1）。
//
// 职责与其它平台一致：文件落地归安装包（MSI / dist 解压），本函数只装配服务。
//
// 与 linux/darwin 的差异：
//   - 可执行文件是 mynet.exe / mynetd.exe；
//   - 服务走 Windows SCM（sc create），gnb 由 mynetd auto_restore 拉起；
//   - 默认前缀 `C:\Program Files\MyNet`（程序文件位；home 是 C:\ProgramData\MyNet）。
//
// 判据对齐 §7.1 D1：不返回 error 桩；每一步真做，做不到的明说。
func installWindows(prefix, home, gateways, gnbBin string, enable, dry bool) int {
	if home == "" {
		home = prefix
	}
	binDir := filepath.Join(prefix, "bin")
	envPath := filepath.Join(home, "mynetd.env")
	mynetExe := filepath.Join(binDir, "mynet.exe")
	mynetdExe := filepath.Join(binDir, "mynetd.exe")

	fmt.Println("将执行以下动作（Windows 服务安装，--dry-run 只看不做）：")
	fmt.Printf("  检查二进制已在位（安装包负责）: %s\\{mynet.exe,mynetd.exe}\n", binDir)
	fmt.Printf("  写 %s  (MYNET_HOME=%s, MYNET_GATEWAYS=%s, MYNET_BIN=%s)\n",
		envPath, home, gateways, mynetExe)
	fmt.Printf("  建 %s\\{conf,var}\n", home)
	if enable {
		fmt.Println("  注册 Windows 服务 mynetd（SCM，start= auto）")
		fmt.Println("  （gnb 不单独立服务：由 mynetd auto_restore_tunnel 拉起）")
		fmt.Println(`  托盘自启: reg add HKLM …\Run\MyNetTray（mynet_tray.exe 在位才注册）`)
		fmt.Println("  装完自动打开 WebUI: http://127.0.0.1:8096/ui/（等端口就绪，失败只提示）")
	}
	if dry {
		fmt.Println("\n--dry-run：以上都没有执行")
		return 0
	}

	// 0) 二进制必须已由安装包放好
	for _, bin := range []string{mynetExe, mynetdExe} {
		if !fileExists(bin) {
			fmt.Fprintf(os.Stderr, "✗ 缺二进制 %s\n", bin)
			fmt.Fprintln(os.Stderr, "  文件落地归安装包（MSI/dist），mynet install 只装服务")
			return 1
		}
	}
	// 0b) 清下载来源标记（浏览器下载 zip 解出来带 MOTW，SCM/Defender 可能拦）
	for _, w := range clearDownloadMarks([]string{mynetExe, mynetdExe}) {
		fmt.Fprintln(os.Stderr, "⚠", w)
	}

	// 1) 停掉可能在跑的 mynetd（服务安装要改 env/注册服务）
	// taskkill 失败可忽略（可能本来就没在跑）
	_ = execCommand("sc", "stop", "mynetd")

	// 2) MYNET_HOME 目录
	for _, sub := range []string{"conf", "var"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	// 3) 环境文件（Windows 服务/计划任务读它）
	if err := writeEnvFile(envPath, home, gateways, binDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// writeEnvFile 写的 MYNET_BIN 不带 .exe，这里补上
	if b, err := os.ReadFile(envPath); err == nil {
		body := string(b)
		body = replaceEnvValue(body, "MYNET_BIN", mynetExe)
		_ = os.WriteFile(envPath, []byte(body), 0o600)
	}
	fmt.Println("installed", envPath)

	// gnb 二进制归 gnb 安装包（mynet_gnb / MSI），这里只校验并写路径。
	// 标准位：driver/gnb/bin/gnb.exe（与 conf 投影同树，resolveGNBBinary 第一优先）。
	gnbDst := filepath.Join(home, "driver", "gnb", "bin", "gnb.exe")
	if gnbBin != "" && fileExists(gnbBin) && gnbBin != gnbDst {
		fmt.Fprintf(os.Stderr, "⚠️ gnb 在 %s，标准位是 %s\n", gnbBin, gnbDst)
		fmt.Fprintln(os.Stderr, "  归位由 gnb 安装包负责；请安装后重跑 mynet install")
	}
	if !fileExists(gnbDst) && !fileExists(gnbBin) {
		fmt.Fprintf(os.Stderr, "⚠️ 未找到 gnb.exe（期望 %s）—— mynet 服务起隧道会失败\n", gnbDst)
	}
	// 环境指向标准位或用户指定路径
	if b, err := os.ReadFile(envPath); err == nil {
		body := replaceEnvValue(string(b), "MYNET_GNB_BIN", gnbDst)
		_ = os.WriteFile(envPath, []byte(body), 0o600)
	}

	fmt.Println("\n下一步（节点身份，install 不代劳）：")
	fmt.Printf("  %s\\mynet.exe setup …\n", binDir)

	if enable {
		if rc := registerWindowsService(home, mynetdExe); rc != 0 {
			return rc
		}
		// 安装器形态（与 macOS 对齐）：托盘随登录起 + 装完立刻开配置页
		installTrayRunKey(binDir, home)
		openWebUIBestEffort()
	} else {
		fmt.Println("（--no-enable：跳过服务注册；稍后可 mynet install 或 sc create）")
	}
	_ = gnbBin
	return 0
}

// trayRunKeyPath 托盘自启注册位。HKLM\...\Run = **每个用户登录都拉**，
// 语义对齐 macOS 的 /Library/LaunchAgents —— 托盘是 GUI 进程，
// 不能塞进 mynetd 服务（session 0 里没有桌面）。
const trayRunKeyPath = `HKLM\Software\Microsoft\Windows\CurrentVersion\Run`

// installTrayRunKey 注册托盘登录自启。mynet_tray.exe 不在位（服务器形态）静默跳过。
func installTrayRunKey(binDir, home string) {
	tray := filepath.Join(binDir, "mynet_tray.exe")
	if !fileExists(tray) {
		return
	}
	// -home 显式传：注册表 Run 给不了环境变量，而托盘的默认猜测
	// （ProgramData\MyNet）与 install 的 home（默认 Program Files\MyNet）不同树 ——
	// intent 文件不同树，托盘的启停按钮就是摆设。
	data := `"` + tray + `" -home "` + home + `"`
	out, err := exec.Command("reg", "add", trayRunKeyPath, "/v", "MyNetTray",
		"/t", "REG_SZ", "/d", data, "/f").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ 托盘自启注册失败（不影响服务）: %v %s\n", err, strings.TrimSpace(string(out)))
		return
	}
	fmt.Println(`托盘自启已注册: HKLM …\Run\MyNetTray（用户登录即出现在通知区）`)
}

// openWebUIBestEffort 等本机 API 就绪后弹默认浏览器打开引导页。
// 失败/超时只提示，不 fail 安装。
// ⚠️ 若本进程是 MSI custom action（SYSTEM、session 0），start 会开在看不见的会话里 ——
// 那种形态归 MSI 收尾阶段以交互用户执行（packaging/windows 待建时处理）。
func openWebUIBestEffort() {
	ready := false
	for i := 0; i < 12; i++ {
		c, err := net.DialTimeout("tcp", DefaultLocalAPI, time.Second)
		if err == nil {
			_ = c.Close()
			ready = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	url := "http://" + DefaultLocalAPI + "/ui/"
	if !ready {
		fmt.Println("⚠ API 端口未就绪，未自动打开浏览器 —— 手动访问", url)
		return
	}
	if err := exec.Command("cmd", "/c", "start", "", url).Run(); err != nil {
		fmt.Println("⚠ 自动打开失败:", err, "—— 手动访问", url)
	}
}

// registerWindowsService 注册/更新 mynetd 服务（SCM）。
//
// 取代计划任务：服务可 Get-Service、失败可自动重启。
// ⚠️ sc.exe 参数必须是 `"start=" "auto"` 两段（`=` 后有空格是 sc 的解析规则）；
// 经 PowerShell 调 sc 会被吃引号 —— 这里用 exec.Command 直接传 argv。
// 环境变量由服务进程自己读 mynetd.env（cmd/mynetd/service_windows.go）。
func registerWindowsService(home, exe string) int {
	// 先清旧服务/计划任务，避免重名
	_ = execCommand("sc.exe", "stop", "mynetd")
	_ = execCommand("sc.exe", "delete", "mynetd")
	_ = execCommand("schtasks", "/Delete", "/F", "/TN", "mynetd")
	_ = execCommand("schtasks", "/Delete", "/F", "/TN", "mynet-gnb")

	binPath := exe
	if strings.ContainsAny(exe, " \t") {
		binPath = `"` + exe + `"`
	}
	out, err := exec.Command("sc.exe", "create", "mynetd",
		"start=", "auto",
		"binPath=", binPath,
		"DisplayName=", "MyNet Daemon",
	).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sc create mynetd: %v\n%s\n", err, out)
		return 1
	}
	// 失败自动重启（1s/5s/30s），与 systemd Restart=always 同等
	_ = exec.Command("sc.exe", "failure", "mynetd",
		"reset=", "86400",
		"actions=", "restart/1000/restart/5000/restart/30000",
	).Run()

	if out, err := exec.Command("sc.exe", "start", "mynetd").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "sc start mynetd: %v\n%s\n", err, out)
		fmt.Println("  服务已注册但启动失败 —— 查事件查看器或 `sc query mynetd`")
		return 1
	}
	fmt.Println("installed service mynetd（start= auto，失败自动重启）")
	fmt.Println("  gnb 由 mynetd 的 auto_restore_tunnel 随服务拉起（agent.json）")
	_ = home
	return 0
}

// uninstallWindowsService 停删 mynetd 服务、清托盘自启与 env 文件。
// home 下的 conf/var 是业务数据，与其它平台口径一致：**不动**。
func uninstallWindowsService(home string) error {
	_ = execCommand("sc.exe", "stop", "mynetd")
	var firstErr error
	if out, err := exec.Command("sc.exe", "delete", "mynetd").CombinedOutput(); err != nil {
		// 服务本就不存在（从没 enable 过）不算错；其余报错留给上层打警告
		if !strings.Contains(string(out), "1060") { // ERROR_SERVICE_DOES_NOT_EXIST
			firstErr = fmt.Errorf("sc delete mynetd: %v %s", err, strings.TrimSpace(string(out)))
		}
	} else {
		fmt.Println("deleted service mynetd")
	}
	if err := exec.Command("reg", "delete", trayRunKeyPath, "/v", "MyNetTray", "/f").Run(); err == nil {
		fmt.Println(`removed 托盘自启 HKLM …\Run\MyNetTray`)
	}
	p := filepath.Join(home, "mynetd.env")
	if err := os.Remove(p); err == nil {
		fmt.Println("removed", p)
	}
	return firstErr
}

// replaceEnvFile 里 MYNET_BIN 要带 .exe；writeEnvFile 是 linux 口径，
// 这里做一次定点替换，不去改 writeEnvFile（它的调用方还有 systemd）。
func replaceEnvValue(body, key, val string) string {
	lines := splitEnvLines(body)
	for i, line := range lines {
		if len(line) >= len(key)+1 && line[:len(key)] == key && line[len(key)] == '=' {
			lines[i] = key + "=" + val
		}
	}
	out := ""
	for i, line := range lines {
		if i > 0 {
			out += "\n"
		}
		out += line
	}
	return out
}

func splitEnvLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start <= len(s) {
		out = append(out, s[start:])
	}
	return out
}

// execCommand 失败不关心（taskkill 在进程不存在时非 0）。
func execCommand(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// platformInstall 是 `mynet install` 的 Windows 入口（cmd_install.go 调用）。
func platformInstall(prefix, home, gateways, gnbBin string, enable, dry bool) int {
	return installWindows(prefix, home, gateways, gnbBin, enable, dry)
}

func platformDescriptor() PlatformDescriptor {
	return PlatformDescriptor{
		Platform:     "windows",
		PkgManager:   "nsis/msi",
		ServiceMgr:   "scm",
		Capabilities: []string{"service.scm", "tray", "apply.osroute"},
	}
}

// platformUninstall 是 `mynet uninstall` 的 Windows 入口。
// 不做 Geteuid 门禁：Windows 无 POSIX uid，sc/reg 自会在权限不足时报错
// （与安装侧口径一致 —— installWindows 也不设门禁）。
func platformUninstall(home string, keepBin, dry bool) int {
	fmt.Println("将执行以下动作（--dry-run 只看不做）：")
	fmt.Println("  sc stop/delete mynetd + 删注册表托盘自启（HKLM …\\Run\\MyNetTray）")
	fmt.Printf("  删 %s\\mynetd.env\n", home)
	if !keepBin {
		fmt.Printf("  删 %s\\bin\\{mynet.exe,mynetd.exe}\n", home)
	}
	fmt.Printf("  **保留** %s\\{conf,var}（节点身份与密钥，卸载不替你丢业务数据）\n", home)
	if dry {
		fmt.Println("\n--dry-run：以上都没有执行")
		return 0
	}
	if err := uninstallWindowsService(home); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ uninstall 服务/托盘自启: %v（继续删文件）\n", err)
	}
	removeOwnBinaries(home, keepBin)
	uninstallTail(home)
	return 0
}
