package mynet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// installWindows — Windows 安装（V5-2 / T5-1）。
//
// 与 linux/darwin 的差异：
//   - 可执行文件是 mynet.exe / mynetd.exe；
//   - 服务走 Windows SCM（sc create），gnb 由 mynetd auto_restore 拉起；
//   - 默认前缀 `C:\Program Files\MyNet`（与 INSTALL.md / PlatformDefaultHome 一致）。
//
// 判据对齐 §7.1 D1：不返回 error 桩；每一步真做，做不到的明说。
func installWindows(prefix, home, gateways, gnbBin string, enable, dry bool) int {
	if home == "" {
		home = prefix
	}
	binDir := filepath.Join(prefix, "bin")
	envPath := filepath.Join(home, "mynetd.env")

	fmt.Println("将执行以下动作（--dry-run 只看不做）：")
	fmt.Printf("  写 %s  (MYNET_HOME=%s, MYNET_GATEWAYS=%s, MYNET_BIN=%s\\mynet.exe)\n",
		envPath, home, gateways, binDir)
	fmt.Printf("  建 %s\\{conf,var}\n", home)
	fmt.Printf("  装二进制到 %s\\{mynet.exe,mynetd.exe}\n", binDir)
	if enable {
		fmt.Println("  注册 Windows 服务 mynetd（SCM，start= auto）")
		fmt.Println("  （gnb 不单独立服务：由 mynetd auto_restore_tunnel 拉起）")
	}
	if dry {
		fmt.Println("\n--dry-run：以上都没有执行")
		return 0
	}

	// 0) 停掉可能在跑的 mynetd/mynet，避免覆盖被占用的 exe
	// taskkill 失败可忽略（可能本来就没在跑）
	_ = execCommand("taskkill", "/F", "/IM", "mynetd.exe")
	_ = execCommand("taskkill", "/F", "/IM", "mynet.exe")
	_ = execCommand("sc", "stop", "mynetd")

	// 1) 二进制（带 .exe）
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "定位自身可执行文件:", err)
		return 1
	}
	if err := copyFile(self, filepath.Join(binDir, "mynet.exe"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "install mynet.exe: %v\n", err)
		return 1
	}
	src := filepath.Join(filepath.Dir(self), "mynetd.exe")
	if fileExists(src) {
		if err := copyFile(src, filepath.Join(binDir, "mynetd.exe"), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "install mynetd.exe: %v\n", err)
			return 1
		}
	} else {
		fmt.Fprintf(os.Stderr, "⚠️ 找不到同目录的 mynetd.exe（%s），只装了 mynet.exe —— 节点守护进程会缺失\n", src)
	}

	// 2) MYNET_HOME 目录
	for _, sub := range []string{"conf", "var"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	// 3) 环境文件（Windows 服务/计划任务以后读它）
	if err := writeEnvFile(envPath, home, gateways, binDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// writeEnvFile 写的 MYNET_BIN 不带 .exe，这里补上
	if b, err := os.ReadFile(envPath); err == nil {
		body := string(b)
		body = replaceEnvValue(body, "MYNET_BIN", filepath.Join(binDir, "mynet.exe"))
		_ = os.WriteFile(envPath, []byte(body), 0o600)
	}
	fmt.Println("installed", envPath)
	fmt.Println("installed", filepath.Join(binDir, "mynet.exe"))
	if fileExists(filepath.Join(binDir, "mynetd.exe")) {
		fmt.Println("installed", filepath.Join(binDir, "mynetd.exe"))
	}

	// gnb 归位到 driver/gnb/bin（与 conf 投影同树，resolveGNBBinary 第一优先）。
	// gnb-win 手装目录不作为长期布局。wintun.dll 必须同目录 —— 缺了 gnb 起来即退。
	if gnbBin != "" && fileExists(gnbBin) {
		dst := filepath.Join(home, "driver", "gnb", "bin", "gnb.exe")
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := copyFile(gnbBin, dst, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "install gnb.exe: %v\n", err)
		} else {
			fmt.Println("installed", dst)
			// 依赖 DLL 与工具：与 gnb.exe 同目录
			srcDir := filepath.Dir(gnbBin)
			for _, extra := range []string{"wintun.dll", "gnb_ctl.exe", "gnb_es.exe", "gnb_crypto.exe"} {
				s := filepath.Join(srcDir, extra)
				if fileExists(s) {
					_ = copyFile(s, filepath.Join(filepath.Dir(dst), extra), 0o755)
				}
			}
			// 环境覆盖也指向标准位
			if b, err := os.ReadFile(envPath); err == nil {
				body := replaceEnvValue(string(b), "MYNET_GNB_BIN", dst)
				_ = os.WriteFile(envPath, []byte(body), 0o600)
			}
		}
	}

	fmt.Println("\n下一步（节点身份，install 不代劳）：")
	fmt.Printf("  %s\\mynet.exe setup …\n", binDir)

	if enable {
		if rc := registerWindowsService(home, filepath.Join(binDir, "mynetd.exe")); rc != 0 {
			return rc
		}
	} else {
		fmt.Println("（--no-enable：跳过服务注册；稍后可 mynet install 或 sc create）")
	}
	_ = gnbBin
	return 0
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
