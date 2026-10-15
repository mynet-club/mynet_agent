//go:build windows

package mynet

import (
	"os/exec"
	"strconv"
	"strings"
)

func gnbExeName() string { return "gnb.exe" }

func gnbExtraDirs() []string {
	return []string{`C:\mynet\bin`}
}

func pidAlive(pid int) bool {
	// Windows 没有 signal 0；用 tasklist 粗查
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

func osRouteExists(dest string) bool {
	out, err := exec.Command("route", "print", "-4").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), dest)
}

func localRouteExists(host string) bool {
	return osRouteExists(host)
}
