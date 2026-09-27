//go:build !windows

package localapi

import "syscall"

// pidAlive 存在性检查：signal 0 不发信号，只问「这个 pid 还在不在」。
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
