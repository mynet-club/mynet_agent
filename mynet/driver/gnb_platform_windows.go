//go:build windows

package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// gnbExeName Windows 可执行文件带 .exe。
func gnbExeName() string { return "gnb.exe" }

// gnbPlatformConfKeys Windows 必须 if-drv wintun，否则默认 TAP/DEFAULT 建不出网卡
// （183 实测：进程起了、9016 在听，但 Get-NetAdapter 没有隧道口）。
func gnbPlatformConfKeys() string { return "if-drv wintun\n" }

// gnbDaemonLine Windows：**必须 daemon off** —— daemon on 会让 gnb 起完就退。
// 无 fork，由 spawnDetached 不 Wait 托住。
func gnbDaemonLine() string { return "daemon off\n" }

// pidAliveOS Windows 无 kill -0；tasklist 有该 PID 即活。
func (d *GNBDriver) pidAliveOS(pid int) bool {
	out, err := d.Exec("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH")
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

// pidKillOS Windows 无信号语义；按 PID taskkill（仍不按名字杀）。
func (d *GNBDriver) pidKillOS(pid int) error {
	_, err := d.Exec("taskkill", "/F", "/PID", strconv.Itoa(pid))
	return err
}

// spawnGnb Windows：gnb 不写 gnb.pid 且前台不退 —— 我们记下 PID 再靠它判活。
func (d *GNBDriver) spawnGnb(bin string) (int, error) {
	return spawnDetachedPID(bin, "-c", d.ConfDir)
}

// writePidAfterSpawn Windows 上由我们补 gnb.pid。
func (d *GNBDriver) writePidAfterSpawn(pid int) {
	if pid > 0 {
		_ = os.WriteFile(d.ConfDir+"/gnb.pid", []byte(strconv.Itoa(pid)+"\n"), 0o644)
	}
}

// pidKill9OS Windows 的 taskkill /F 本身就是硬杀（无进程组优雅退出的对应物）。
func (d *GNBDriver) pidKill9OS(pid int) error { return d.pidKillOS(pid) }

// lockLifecycle Windows 无 flock；用「O_EXCL 建锁文件 + 内容记持有者 pid」。
//
// ⚠️ 与 unix 的 flock 不同，O_EXCL 锁文件在**持有者被强杀时不会自动消失** —— 所以必须
// 按内容判陈旧（持有者 pid 已死/内容坏就抢过来），否则一次崩溃就永久卡住后续启停。
func (d *GNBDriver) lockLifecycle() (func(), error) {
	if d.ConfDir == "" {
		return func() {}, nil
	}
	lockPath := filepath.Join(d.ConfDir, ".gnb-lifecycle.lock")
	deadline := time.Now().Add(lifecycleLockTimeout)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if b, rerr := os.ReadFile(lockPath); rerr == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			if pid <= 0 || !d.pidAliveOS(pid) {
				_ = os.Remove(lockPath)
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("gnb 生命周期锁等待超时（%s）：另一个启停/重载仍在进行", lockPath)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// sameConfdirPidsOS 用 WMI 取 gnb.exe 的命令行（tasklist 不给命令行）。
func (d *GNBDriver) sameConfdirPidsOS() []int {
	out, err := d.Exec("powershell", "-NoProfile", "-Command",
		`Get-CimInstance Win32_Process -Filter "name='gnb.exe'" | ForEach-Object { "$($_.ProcessId) $($_.CommandLine)" }`)
	if err != nil {
		return nil
	}
	return parseSameConfdirPids(string(out), d.ConfDir, gnbExeName())
}
