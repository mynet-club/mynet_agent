//go:build windows

package driver

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
