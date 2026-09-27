//go:build windows

package main

import (
	"os/exec"
	"strconv"
)

// Windows：用 taskkill /PID 钉住，不用 /IM 按镜像名。
func unixPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid)).Run()
	return err == nil
}

func killPID(pid int, sig string) error {
	// TERM→温和结束；KILL→/F
	args := []string{"/PID", strconv.Itoa(pid)}
	if sig == "KILL" {
		args = append(args, "/F")
	}
	return exec.Command("taskkill", args...).Run()
}

func setSysProcAttr(cmd *exec.Cmd) {
	// Windows 无需 Setpgid
}
