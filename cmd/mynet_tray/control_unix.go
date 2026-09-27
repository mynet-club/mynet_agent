//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// unixPIDAlive kill(pid, 0) 探活。
func unixPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func killPID(pid int, sig string) error {
	s := syscall.SIGTERM
	if sig == "KILL" {
		s = syscall.SIGKILL
	}
	return syscall.Kill(pid, s)
}

// setSysProcAttr 新会话/进程组，避免 osascript 子进程与托盘同生共死。
func setSysProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
