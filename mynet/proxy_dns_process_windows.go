//go:build windows

package mynet

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func hookDetachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000200 | 0x08000000, HideWindow: true}
}
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == 259
}

func lockDNSHookControl(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

func hookWorkerOwned(pid int) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "(Get-CimInstance Win32_Process -Filter 'ProcessId = "+strconv.Itoa(pid)+"').CommandLine").Output()
	cmd := strings.ToLower(string(out))
	return err == nil && strings.Contains(cmd, strings.ToLower(exe)) && strings.Contains(cmd, "proxy dns-hook start")
}
