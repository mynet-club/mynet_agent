//go:build !windows

package mynet

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func hookDetachedAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

func lockDNSHookControl(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func hookWorkerOwned(pid int) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	out, err := exec.Command("ps", "-ww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	return err == nil && strings.Contains(string(out), exe) && strings.Contains(string(out), "proxy dns-hook start")
}
