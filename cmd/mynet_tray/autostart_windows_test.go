//go:build windows

package main

// registryRunStarter 的实机腿。macOS/Linux 上没有 reg.exe，autostart_test.go
// 只能覆盖 launchAgentStarter；这份文件补上 Windows 那一半。
//
// 跑法（开发机交叉编，测试机执行）：
//
//	cd mynet_agent && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
//	  go test -c -o /tmp/tray_win_test.exe ./cmd/mynet_tray
//	scp /tmp/tray_win_test.exe arthur@192.168.0.5:
//	ssh arthur@192.168.0.5 tray_win_test.exe -test.v -test.run TestRegistryRun
//
// 键用 HKCU 下的临时路径顶替 HKLM：regRunHas/Enable/Disable 只是 shell out
// `reg <verb> <key> /v <name>`，键挂在哪一层不影响形状，要验的是「系统级那份
// 在位时用户级必须拒绝」这个前提。生产键路径另做只读取证（192.168.0.5：
// HKLM ...\Run 有 MyNetTray、HKCU 没有 —— 正是安装器登记系统级、托盘只看
// 用户级会误判「未勾选」的真实形态）。
//
// 键名故意不含 "MyNetTray"：regRunHas 用 strings.Contains 判整段输出，
// 而输出首行就是键路径，同名会让「值不存在」也判成存在。

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

const (
	probeKeyRoot   = `HKCU\Software\MyNetRunProbe`
	probeKeyUser   = probeKeyRoot + `\User`
	probeKeySystem = probeKeyRoot + `\System`
)

// newProbeStarter 返回指向临时键的 starter，并保证测试前后都清干净。
func newProbeStarter(t *testing.T) *registryRunStarter {
	t.Helper()
	_, _ = reg("delete", probeKeyRoot, "/f") // 起点干净；本来就没有会失败，无所谓
	t.Cleanup(func() { _, _ = reg("delete", probeKeyRoot, "/f") })
	return &registryRunStarter{systemKey: probeKeySystem, key: probeKeyUser, name: utils.TrayRunValueName}
}

func reg(args ...string) (string, error) {
	out, err := exec.Command("reg", args...).CombinedOutput()
	return string(out), err
}

func mustReg(t *testing.T, args ...string) {
	t.Helper()
	out, err := reg(args...)
	if err != nil {
		t.Fatalf("reg %v: %v\n%s", args, err, out)
	}
}

// regHas 独立于被测代码再查一遍，避免「用被测函数验证被测函数」。
func regHas(t *testing.T, key string) bool {
	t.Helper()
	out, err := reg("query", key, "/v", utils.TrayRunValueName)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == utils.TrayRunValueName {
			return true
		}
	}
	return false
}

// TestRegistryRunNothingRegistered 两处都没有 ⇒ (false, nil)。
func TestRegistryRunNothingRegistered(t *testing.T) {
	s := newProbeStarter(t)
	if on, err := s.IsEnabled(); on || err != nil {
		t.Errorf("IsEnabled = (%v, %v), want (false, nil)", on, err)
	}
}

// TestRegistryRunMissingValueOnExistingKey 锁 reg query 的退出码语义：
// 键在、值不在，必须判「未设置」而不是报错或误判存在。
func TestRegistryRunMissingValueOnExistingKey(t *testing.T) {
	s := newProbeStarter(t)
	mustReg(t, "add", probeKeyUser, "/v", "Other", "/t", "REG_SZ", "/d", "x", "/f")

	if on, err := s.IsEnabled(); on || err != nil {
		t.Errorf("IsEnabled = (%v, %v), want (false, nil)", on, err)
	}
}

// TestRegistryRunUserOnlyToggle 没有安装器登记时，托盘自己那份要能正常开关。
func TestRegistryRunUserOnlyToggle(t *testing.T) {
	s := newProbeStarter(t)

	if err := s.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !regHas(t, probeKeyUser) {
		t.Fatal("Enable 后用户级键里查不到值")
	}
	if on, err := s.IsEnabled(); !on || err != nil {
		t.Errorf("IsEnabled = (%v, %v), want (true, nil)", on, err)
	}

	if err := s.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if regHas(t, probeKeyUser) {
		t.Error("Disable 后用户级那份还在")
	}
	if on, _ := s.IsEnabled(); on {
		t.Error("Disable 后应判未启用")
	}
}

// TestRegistryRunSeesInstallerEntry 锁 D10/D11 的 Windows 腿：安装器写在
// HKLM，旧版只查 HKCU ⇒ 装完菜单一直未勾选，用户一点就多出跨 hive 的第二份，
// 而且再也关不掉（Disable 只删 HKCU）。
func TestRegistryRunSeesInstallerEntry(t *testing.T) {
	s := newProbeStarter(t)
	mustReg(t, "add", probeKeySystem, "/v", utils.TrayRunValueName, "/t", "REG_SZ", "/d", `"C:\fake\mynet_tray.exe"`, "/f")

	if on, err := s.IsEnabled(); !on || err != nil {
		t.Errorf("系统级在位应判已启用，got (%v, %v)", on, err)
	}
	if err := s.Enable(); !errors.Is(err, errSystemManaged) {
		t.Errorf("Enable = %v, want errSystemManaged", err)
	}
	if regHas(t, probeKeyUser) {
		t.Error("Enable 被拒后不该写出用户级那份 —— 那就是双托盘")
	}
	if err := s.Disable(); !errors.Is(err, errSystemManaged) {
		t.Errorf("Disable = %v, want errSystemManaged（不能让用户以为关掉了）", err)
	}
	if !regHas(t, probeKeySystem) {
		t.Error("不许动系统级那份")
	}
}

// TestRegistryRunDisableClearsLegacyUserEntry 老版本可能已经写过用户级，
// Disable 必须先把它删掉（双托盘才会真的消失），再报系统级仍在位。
func TestRegistryRunDisableClearsLegacyUserEntry(t *testing.T) {
	s := newProbeStarter(t)
	mustReg(t, "add", probeKeySystem, "/v", utils.TrayRunValueName, "/t", "REG_SZ", "/d", `"C:\fake\mynet_tray.exe"`, "/f")
	mustReg(t, "add", probeKeyUser, "/v", utils.TrayRunValueName, "/t", "REG_SZ", "/d", `"C:\fake\mynet_tray.exe"`, "/f")

	if err := s.Disable(); !errors.Is(err, errSystemManaged) {
		t.Errorf("Disable = %v, want errSystemManaged", err)
	}
	if regHas(t, probeKeyUser) {
		t.Error("用户级那份没被清掉 —— 双托盘会留着")
	}
	if !regHas(t, probeKeySystem) {
		t.Error("不许动系统级那份")
	}
}
