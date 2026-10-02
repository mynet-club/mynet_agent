package mynet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// macOS 用 launchd 装系统服务（P3）。
//
// 与 Linux systemd 的对应关系：
//
//	systemd unit              launchd plist                    Label
//	/etc/systemd/system/…     /Library/LaunchDaemons/….plist   com.mynet.*
//	systemctl enable --now    launchctl bootstrap system / load
//	systemctl disable --now   launchctl bootout system
//	EnvironmentFile           EnvironmentVariables（写进 plist）
//
// ⚠️ 只装 mynetd 一个 LaunchDaemon。gnb **不进 launchd**，由 mynetd 的
// 生命周期（bundle_changed → service reload）拉起：
//
//  1. gnb 的 `--systemd`（保持前台）只在 __linux__ 编译
//     （gnb_argv.c:289-291 的 `#if defined(__linux__)`），darwin 上
//     `gnb --systemd` 直接 `unrecognized option`；
//  2. 而守卫 B1 禁止直改 mynet_gnb/src，不能把这个 flag 放开到全平台；
//  3. gnb 的 `daemon on` 自我 daemon 化后 launchd 看不见主进程，
//     KeepAlive 会反复拉起 → 实测 4 个 gnb 抢同一端口
//     （`bind: Address already in use`）。
//
// 所以 launchd 负责「mynetd 一直活着」，gnb 由 mynetd 用 daemon 化方式拉起
// —— 与 Linux 的 mynet_gnb.service 分工不同，这是 darwin 的现实约束。

// LaunchdLabel LaunchDaemon 的 Label。gnb 的那条只在卸载时清理
// （旧版装过），不再写入。LabelTray 与托盘自带 autostart 同名 ——
// 同一 gui 域里同名 Label 只会有一份生效，不会双托盘。
const (
	LabelMynetd = "com.mynet.mynetd"
	LabelGNB    = "com.mynet.gnb"
	LabelTray   = "club.mynet.tray"
)

// launchdPlistDir root 专属目录；launchAgentsDir 是全体登录用户的
// LaunchAgent 目录（launchd 在每次登录时自动加载，安装器代写=装完即有托盘）。
const (
	launchdPlistDir = "/Library/LaunchDaemons"
	launchAgentsDir = "/Library/LaunchAgents"
)

// installLaunchd 写 plist 并 bootstrap。要求已 root。
func installLaunchd(home, gateways, binDir, gnbBin string, enable bool) error {
	if err := os.MkdirAll("/var/log/mynet", 0o755); err != nil {
		return err
	}
	mynetdBin := filepath.Join(binDir, "mynetd")
	env := map[string]string{
		"MYNET_HOME":     home,
		"MYNET_GATEWAYS": gateways,
		"MYNET_BIN":      filepath.Join(binDir, "mynet"),
		// route/ifconfig 在 /sbin，launchd 的默认 PATH 不含它 ——
		// 与 apply/node-status 踩过的是同一个坑
		"PATH": "/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}

	// 8096 = 本机 API/WebUI 约定端口（AGENTS.md §1.5）；与 DefaultLocalAPI 一致
	if err := writeLaunchdPlist(LabelMynetd, launchdPlistDir, []string{mynetdBin, "-listen", "127.0.0.1:8096"},
		env, "/var/log/mynet/mynetd.log", "/var/log/mynet/mynetd.err", true); err != nil {
		return err
	}
	fmt.Println("installed", filepath.Join(launchdPlistDir, LabelMynetd+".plist"))
	fmt.Println("  （gnb 不进 launchd：由 mynetd 生命周期拉起，原因见源码注释）")

	if enable {
		_, _ = launchctl("bootout", "system/"+LabelMynetd)
		if out, err := launchctl("bootstrap", "system", filepath.Join(launchdPlistDir, LabelMynetd+".plist")); err != nil {
			return fmt.Errorf("launchctl bootstrap mynetd: %v %s", err, out)
		}
		fmt.Println("bootstrapped: " + LabelMynetd)
	}
	_ = gnbBin
	return nil
}

// installTrayAgent 装 macOS 托盘（安装器形态的一部分：装完菜单栏就要有东西）。
//
// 托盘是 GUI 进程，归 LaunchAgent（gui 域）不归 LaunchDaemon：
//   - plist 写 /Library/LaunchAgents —— 全体用户登录时 launchd 自动加载；
//   - 当场生效要 `launchctl asuser <uid> bootstrap gui/<uid>`（root 装完
//     立刻拉给控制台用户，不用等下次登录）；
//   - KeepAlive=false：用户从菜单退出托盘就该是退出，launchd 复活是骚扰。
//
// mynet_tray 不在位（服务器/精简分发）静默跳过 —— 服务本身照装。
// enable=false 只写 plist 不 bootstrap（与 installLaunchd 对 mynetd 的分工一致）。
func installTrayAgent(home, binDir string, enable bool) error {
	tray := filepath.Join(binDir, "mynet_tray")
	if !fileExists(tray) {
		return nil
	}
	env := map[string]string{
		"MYNET_HOME": home,
		"PATH":       "/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	// gui 域以登录用户身份跑：/var/log/mynet 是 root 0755 写不进去，
	// 日志给 /dev/null（托盘状态以 WebUI/mynetd 为准，不靠这份日志）
	plist := filepath.Join(launchAgentsDir, LabelTray+".plist")
	if err := writeLaunchdPlist(LabelTray, launchAgentsDir, []string{tray},
		env, "/dev/null", "/dev/null", false); err != nil {
		return err
	}
	fmt.Println("installed", plist)
	if !enable {
		fmt.Println("  （--no-enable：plist 已就位，未 bootstrap）")
		return nil
	}
	uid := consoleUserUID()
	if uid == "" {
		fmt.Println("  （无 GUI 登录会话：plist 已就位，下次登录自动拉起托盘）")
		return nil
	}
	_, _ = launchctl("asuser", uid, "launchctl", "bootout", "gui/"+uid+"/"+LabelTray)
	if out, err := launchctl("asuser", uid, "launchctl", "bootstrap", "gui/"+uid, plist); err != nil {
		fmt.Printf("⚠ 托盘 bootstrap 失败（下次登录仍会拉起）: %v %s\n", err, strings.TrimSpace(out))
	} else {
		fmt.Println("tray bootstrapped: gui/" + uid + "/" + LabelTray)
	}
	return nil
}

// consoleUserUID 当前 GUI 登录用户的 uid；无人登录（或登录的是 root）返回 ""。
// `stat -f%Su /dev/console` 是判定控制台用户的标准姿势（$USER/SUDO_USER 都不可信）。
func consoleUserUID() string {
	out, err := exec.Command("stat", "-f%Su", "/dev/console").Output()
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(out))
	if name == "" || name == "root" {
		return ""
	}
	u, err := exec.Command("id", "-u", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(u))
}

// uninstallLaunchd bootout 并删 plist。**不动** home 下的节点身份与数据。
func uninstallLaunchd(keepBin bool, home string) error {
	for _, label := range []string{LabelGNB, LabelMynetd} {
		_, _ = launchctl("bootout", "system/"+label)
		p := filepath.Join(launchdPlistDir, label+".plist")
		if err := os.Remove(p); err == nil {
			fmt.Println("removed", p)
		}
	}
	// 托盘在 gui 域：先按控制台用户 bootout 再删 plist；
	// 其它用户的 gui 域不用管 —— plist 没了，下次登录自然不加载。
	if uid := consoleUserUID(); uid != "" {
		_, _ = launchctl("asuser", uid, "launchctl", "bootout", "gui/"+uid+"/"+LabelTray)
	}
	p := filepath.Join(launchAgentsDir, LabelTray+".plist")
	if err := os.Remove(p); err == nil {
		fmt.Println("removed", p)
	}
	_ = keepBin
	_ = home
	return nil
}

// writeLaunchdPlist 写一份 plist（LaunchDaemon 与 LaunchAgent 共用模板）。
// 路径写进 ProgramArguments（绝对路径，不靠 PATH —— launchd 的 PATH 是残的）。
// keepAlive=false 用于交互式进程（托盘）：RunAtLoad 拉一次，用户退出就是退出。
func writeLaunchdPlist(label, dir string, argv []string, env map[string]string, stdout, stderr string, keepAlive bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ka := "<false/>"
	if keepAlive {
		ka = "<true/>"
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + xmlEsc(label) + `</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, a := range argv {
		b.WriteString("    <string>" + xmlEsc(a) + "</string>\n")
	}
	b.WriteString(`  </array>
  <key>EnvironmentVariables</key>
  <dict>
`)
	// 稳定顺序，便于 diff
	for _, k := range []string{"MYNET_HOME", "MYNET_GATEWAYS", "MYNET_BIN", "PATH"} {
		if v, ok := env[k]; ok {
			b.WriteString("    <key>" + xmlEsc(k) + "</key>\n")
			b.WriteString("    <string>" + xmlEsc(v) + "</string>\n")
		}
	}
	b.WriteString(`  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  ` + ka + `
  <key>StandardOutPath</key>
  <string>` + xmlEsc(stdout) + `</string>
  <key>StandardErrorPath</key>
  <string>` + xmlEsc(stderr) + `</string>
</dict>
</plist>
`)
	return os.WriteFile(filepath.Join(dir, label+".plist"), []byte(b.String()), 0o644)
}

func launchctl(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	return string(out), err
}

// xmlEsc plist 是 XML，路径/URL 里的 & < > 必须转义，
// 否则 launchctl 读到一半就拒绝。
func xmlEsc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
