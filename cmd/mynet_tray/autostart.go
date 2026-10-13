package main

// 开机自启：登录后拉起托盘本身（不是 mynetd —— 那归 launchd/计划任务）。
//
//	macOS   → ~/Library/LaunchAgents/club.mynet.tray.plist
//	Windows → HKCU\...\Run "MyNetTray"
//	其他    → no-op（开发机不强求）

import (
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

// NewAutoStarter 按平台返回。
func NewAutoStarter() AutoStarter {
	switch runtime.GOOS {
	case "darwin":
		return &launchAgentStarter{}
	case "windows":
		return &registryRunStarter{key: `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, name: "MyNetTray"}
	default:
		return &noopStarter{}
	}
}

// ── macOS LaunchAgent ────────────────────────────────────────

type launchAgentStarter struct{}

func (s *launchAgentStarter) plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", trayLabel+".plist")
}

func (s *launchAgentStarter) IsEnabled() (bool, error) {
	_, err := os.Stat(s.plistPath())
	return err == nil, nil
}

func (s *launchAgentStarter) Enable() error {
	self := selfPath()
	if self == "" {
		return fmt.Errorf("无法确定自身路径")
	}
	dir := filepath.Dir(s.plistPath())
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
	if err := os.WriteFile(s.plistPath(), []byte(body), 0o644); err != nil {
		return err
	}
	// 当场 load，失败不致命（下次登录仍会拉）
	_ = runOK("launchctl", "load", "-w", s.plistPath())
	return nil
}

func (s *launchAgentStarter) Disable() error {
	_ = runOK("launchctl", "unload", "-w", s.plistPath())
	if err := os.Remove(s.plistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ── Windows 注册表 Run ───────────────────────────────────────

type registryRunStarter struct {
	key  string
	name string
}

func (s *registryRunStarter) IsEnabled() (bool, error) {
	// reg query 失败 = 未设置
	out, err := exec.Command("reg", "query", s.key, "/v", s.name).CombinedOutput()
	if err != nil {
		return false, nil
	}
	return strings.Contains(string(out), s.name), nil
}

func (s *registryRunStarter) Enable() error {
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
