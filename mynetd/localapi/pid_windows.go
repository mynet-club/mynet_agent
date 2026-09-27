//go:build windows

package localapi

import "syscall"

// STILL_ACTIVE —— GetExitCodeProcess 在进程仍运行时返回的退出码。
const stillActive = 259

// pidAlive 存在性检查。Windows 没有 signal 0，改用 OpenProcess +
// GetExitCodeProcess：句柄打得开且退出码是 STILL_ACTIVE 才算活着。
//
// 不能用 os.FindProcess —— Windows 上它对任意 pid 都「成功」。
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
