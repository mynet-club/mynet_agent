package main

// AutoStarter 平台腿的测试。
//
// App 层只认 AutoStarter 接口（用 fakeAutoStarter 驱动，见 intent_test.go），
// 这里测**真实 launchAgentStarter**，覆盖两件事：
//
//  1. 「读不到」必须报错，不能当成「关着」。旧版 `return err == nil, nil`
//     把权限错误一起吞了（D10），菜单于是显示未勾选，用户再点一次会去
//     Enable 一个可能已经开着的东西。
//  2. 安装器登记的是**系统级** /Library/LaunchAgents，托盘只能写用户级。
//     生效状态必须两处都看，且系统级在位时拒绝用户级切换（D10/D11）。
//     systemDir 做成字段就是为了这里能指到临时目录 —— 测试不可能往
//     /Library/LaunchAgents 写东西。
//
// registryRunStarter 的同形状逻辑在 `autostart_windows_test.go`（//go:build
// windows）：它靠 reg.exe，本机跑不了，得 `GOOS=windows go test -c` 出 exe
// 拿到真机执行 —— 已在 192.168.0.5 跑通 5/5（见 playbook §7 的核对记录）。

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLaunchAgentIsEnabled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("userPlistPath 走 os.UserHomeDir，Windows 上不看 HOME，隔离不住")
	}
	isolateUserConfig(t)
	s := &launchAgentStarter{systemDir: filepath.Join(t.TempDir(), "LaunchAgents")}

	if on, err := s.IsEnabled(); on || err != nil {
		t.Errorf("两处都没有 plist 应是 (false, nil)，got (%v, %v)", on, err)
	}

	if err := os.MkdirAll(filepath.Dir(s.userPlistPath()), 0o755); err != nil {
		t.Fatalf("%v", err)
	}
	if err := os.WriteFile(s.userPlistPath(), []byte("<plist/>\n"), 0o644); err != nil {
		t.Fatalf("%v", err)
	}
	if on, err := s.IsEnabled(); !on || err != nil {
		t.Errorf("用户级 plist 在位应是 (true, nil)，got (%v, %v)", on, err)
	}
}

// TestLaunchAgentSeesInstallerEntry 锁 D10 的主症状：安装器写的是系统级
// /Library/LaunchAgents，旧版只 os.Stat 用户级 ⇒ 装完菜单一直显示未勾选，
// 用户再点一次就多出第二份。
func TestLaunchAgentSeesInstallerEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("同上")
	}
	isolateUserConfig(t)
	s := newTestLaunchAgentStarter(t)
	writePlist(t, s.systemPlistPath())

	on, err := s.IsEnabled()
	if !on || err != nil {
		t.Errorf("系统级在位应判已启用，got (%v, %v)", on, err)
	}
	// 系统级已在位时不许再写用户级：那只会造出同名第二份。
	if err := s.Enable(); !errors.Is(err, errSystemManaged) {
		t.Errorf("Enable = %v, want errSystemManaged", err)
	}
	if _, statErr := os.Stat(s.userPlistPath()); !os.IsNotExist(statErr) {
		t.Errorf("Enable 被拒后不该留下用户级 plist（stat err=%v）", statErr)
	}
	// 关闭：用户级本来就没有，仍要报「系统级改不了」，不能让用户以为关掉了。
	if err := s.Disable(); !errors.Is(err, errSystemManaged) {
		t.Errorf("Disable = %v, want errSystemManaged", err)
	}
}

// TestLaunchAgentDisableClearsLegacyUserEntry 锁「用户永远关不掉」那一半：
// 老版本可能已经写过用户级那份，Disable 必须先把它删掉（否则双托盘继续），
// 再报系统级仍在位。
func TestLaunchAgentDisableClearsLegacyUserEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("同上")
	}
	isolateUserConfig(t)
	s := newTestLaunchAgentStarter(t)
	writePlist(t, s.systemPlistPath())
	writePlist(t, s.userPlistPath())

	if err := s.Disable(); !errors.Is(err, errSystemManaged) {
		t.Errorf("Disable = %v, want errSystemManaged", err)
	}
	if _, err := os.Stat(s.userPlistPath()); !os.IsNotExist(err) {
		t.Errorf("用户级那份没被清掉（stat err=%v）—— 双托盘会留着", err)
	}
	if _, err := os.Stat(s.systemPlistPath()); err != nil {
		t.Errorf("不许动系统级那份: %v", err)
	}
}

// TestLaunchAgentUserOnlyToggle 没有安装器登记时，托盘自己那份要能正常开关。
func TestLaunchAgentUserOnlyToggle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("同上")
	}
	isolateUserConfig(t)
	s := newTestLaunchAgentStarter(t)

	if err := s.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if on, _ := s.IsEnabled(); !on {
		t.Fatal("Enable 后应判已启用")
	}
	if err := s.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if on, _ := s.IsEnabled(); on {
		t.Error("Disable 后应判未启用")
	}
}

func newTestLaunchAgentStarter(t *testing.T) *launchAgentStarter {
	t.Helper()
	return &launchAgentStarter{systemDir: filepath.Join(t.TempDir(), "LaunchAgents")}
}

func writePlist(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("%v", err)
	}
	if err := os.WriteFile(p, []byte("<plist/>\n"), 0o644); err != nil {
		t.Fatalf("%v", err)
	}
}

// TestLaunchAgentIsEnabledReportsUnreadable 是 D10 的落点断言。
//
// 把 ~/Library 做成普通文件，Stat(plist) 就会返回 ENOTDIR —— 一个「查不出来」
// 的错误，而不是「没这个文件」。两者必须分开。
func TestLaunchAgentIsEnabledReportsUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("同上：Windows 上隔离不住 os.UserHomeDir")
	}
	isolateUserConfig(t)
	home := os.Getenv("HOME")
	if err := os.WriteFile(filepath.Join(home, "Library"), []byte("not a dir\n"), 0o600); err != nil {
		t.Fatalf("%v", err)
	}

	s := newTestLaunchAgentStarter(t)
	on, err := s.IsEnabled()
	if err == nil {
		t.Fatalf("读不到 plist 必须报错，不能当成「关着」(on=%v)", on)
	}
	if on {
		t.Error("报错时 on 应为 false，别让调用方以为它开着")
	}
	// 文案出口那侧会把它翻成 autoTitleUnknown，而不是「开机启动」（未勾选）。
	if got := autoTitleFor(on, err); got != autoTitleUnknown {
		t.Errorf("autoTitleFor = %q, want %q", got, autoTitleUnknown)
	}
}
