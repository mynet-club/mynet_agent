//go:build !windows

package mynet

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// gnbExeName 平台可执行名。
func gnbExeName() string { return "gnb" }

// gnbExtraDirs 非 Windows 的 gnb 常见安装位。
func gnbExtraDirs() []string {
	return []string{"/usr/bin", "/usr/local/bin", "/opt/mynet/bin"}
}

// pidAlive 存在性检查：signal 0 不发信号，只问「这个 pid 还在不在」。
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// osRouteExists 查系统路由表里有没有这个目的地。
func osRouteExists(dest string) bool {
	bin := lookPathAny("ip")
	if bin == "" {
		bin = lookPathAny("route")
	}
	if bin == "" {
		return false
	}
	var out []byte
	var err error
	if strings.HasSuffix(bin, "ip") {
		out, err = exec.Command(bin, "route", "show", dest).Output()
	} else {
		out, err = exec.Command(bin, "-n", "get", dest).Output()
	}
	if err != nil {
		return false
	}
	return len(out) > 0 && !strings.Contains(string(out), "not in table") && !strings.Contains(string(out), "No route")
}

// localRouteExists 判断目标是否已经由物理网卡承载（非隧道口）。
func localRouteExists(host string) bool {
	return darwinRouteGet(host) || osRouteExists(host)
}

func darwinRouteGet(host string) bool {
	bin := lookPathAny("route")
	if bin == "" {
		return false
	}
	out, err := exec.Command(bin, "-n", "get", host).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "interface:")
}
