package main

// 开机自启：登录后拉起托盘本身（不是 mynetd —— 那归 launchd/计划任务）。
//
//	macOS   → ~/Library/LaunchAgents/club.mynet.tray.plist
//	Windows → HKCU\...\Run "MyNetTray"
//	其他    → no-op（开发机不强求）
//
// ⚠️ 但**安装器登记的是系统级那份**（/Library/LaunchAgents、HKLM Run），
// 托盘（登录用户）只能写用户级。所以「是否已自启」必须两处都看，否则装完
// 菜单一直显示未勾选；而用户级切换在系统级已在位时必须拒绝，否则 Windows
// 上会造出跨 hive 的第二份（双托盘），且用户永远关不掉（D10/D11）。

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// trayLabel 与 install 写入的 LaunchAgent label 同名（shared SSOT）——
// 同一 gui 域同名 Label 只会有一份生效，不会双托盘。
const trayLabel = utils.LaunchdLabelTray

// errSystemManaged 系统级登记在位，托盘无权改。
//
// 这是我们自己造的错误 ⇒ 文案是安全的，可以直接上屏（区别于 launchctl/reg
// 的原始输出，那些含路径与 stderr，只能给固定短句）。见 tray.go 的 autoFailText。
var errSystemManaged = errors.New("开机启动已由安装器在系统级登记，托盘改不了")

// NewAutoStarter 按平台返回。
func NewAutoStarter() AutoStarter {
	switch runtime.GOOS {
	case "darwin":
		return &launchAgentStarter{systemDir: utils.LaunchAgentDirSystem}
	case "windows":
		return &registryRunStarter{systemKey: utils.TrayRunKeySystem, key: utils.TrayRunKeyUser, name: utils.TrayRunValueName}
	default:
		return &noopStarter{}
	}
}

// ── macOS LaunchAgent ────────────────────────────────────────

type launchAgentStarter struct {
	// systemDir 安装器代写的系统级目录。做成字段只为可测 ——
	// 测试不可能往 /Library/LaunchAgents 里写东西。
	systemDir string
}

func (s *launchAgentStarter) userPlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", trayLabel+".plist")
}

func (s *launchAgentStarter) systemPlistPath() string {
	return filepath.Join(s.systemDir, trayLabel+".plist")
}

func (s *launchAgentStarter) IsEnabled() (bool, error) {
	sys, err := plistExists(s.systemPlistPath())
	if err != nil {
		return false, err
	}
	user, err := plistExists(s.userPlistPath())
	if err != nil {
		return false, err
	}
	return sys || user, nil
}

// plistExists 分开「不存在」与「查不出来」：后者必须报错（D10）。
// 旧版 `return err == nil, nil` 把权限错误也吞成 false，菜单于是显示未勾选，
// 用户再点一次会去 Enable 一个可能已经开着的东西。
func plistExists(p string) (bool, error) {
	_, err := os.Stat(p)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

func (s *launchAgentStarter) Enable() error {
	if sys, _ := plistExists(s.systemPlistPath()); sys {
		return errSystemManaged
	}
	self := selfPath()
	if self == "" {
		return fmt.Errorf("无法确定自身路径")
	}
	dir := filepath.Dir(s.userPlistPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><false/>
</dict>
</plist>
`, trayLabel, xmlEsc(self))
	if err := os.WriteFile(s.userPlistPath(), []byte(body), 0o644); err != nil {
		return err
	}
	// 当场 load，失败不致命（下次登录仍会拉）
	_ = runOK("launchctl", "load", "-w", s.userPlistPath())
	return nil
}

func (s *launchAgentStarter) Disable() error {
	// 用户级那份**照删**：老版本可能已经写过，留着就是双托盘。
	_ = runOK("launchctl", "unload", "-w", s.userPlistPath())
	if err := os.Remove(s.userPlistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	// 删完再报系统级仍在位 —— 自启没真的关掉，不能让用户以为关了。
	if sys, _ := plistExists(s.systemPlistPath()); sys {
		return errSystemManaged
	}
	return nil
}

// ── Windows 注册表 Run ───────────────────────────────────────

type registryRunStarter struct {
	systemKey string // 安装器写的 HKLM
	key       string // 托盘自己能读写的 HKCU
	name      string
}

func (s *registryRunStarter) IsEnabled() (bool, error) {
	return regRunHas(s.systemKey, s.name) || regRunHas(s.key, s.name), nil
}

// regRunHas reg query 失败 = 未设置。
//
// 「值不存在」那一支已在真机实测（192.168.0.5，见 autostart_windows_test.go）。
// ⚠️ 但这条在 Windows 上仍把「读不到」当「关着」，是 D10 未修的一小截：
// reg query 对「值不存在」和「注册表访问被拒」都返回非 0，靠退出码分不开。
// 要修得解析 ERROR 文本，或改用 golang.org/x/sys/windows/registry 直接
// 区分 ERROR_FILE_NOT_FOUND。
func regRunHas(key, name string) bool {
	out, err := exec.Command("reg", "query", key, "/v", name).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), name)
}

func (s *registryRunStarter) Enable() error {
	if regRunHas(s.systemKey, s.name) {
		return errSystemManaged
	}
	self := selfPath()
	if self == "" {
		return fmt.Errorf("无法确定自身路径")
	}
	// 路径可能带空格，reg add 的 /d 需要引号
	return runOK("reg", "add", s.key, "/v", s.name, "/t", "REG_SZ", "/d", `"`+self+`"`, "/f")
}

func (s *registryRunStarter) Disable() error {
	// 不存在时 reg delete 返回非 0，对「关闭」来说是成功
	_ = runOK("reg", "delete", s.key, "/v", s.name, "/f")
	if regRunHas(s.systemKey, s.name) {
		return errSystemManaged
	}
	return nil
}

// ── no-op ────────────────────────────────────────────────────

type noopStarter struct{}

func (s *noopStarter) IsEnabled() (bool, error) { return false, nil }
func (s *noopStarter) Enable() error            { return nil }
func (s *noopStarter) Disable() error           { return nil }

func xmlEsc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// runOK 执行平台登记命令（launchctl / reg）。不存在的服务等不视作致命时由调用方判。
//
// ⚠️ 返回的 error 含命令原始 stdout/stderr，**不得直接进 UI**；
// 展示前必须过 safeText（P3/D20）。
func runOK(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}
