//go:build !windows

package mynet

import (
	"net"
	"os/exec"
	"strings"
)

// destOwnerDev 目的地当前挂在哪块网卡上；查不到返回 ""。
// macOS：`route -n get [-net] dest` 的 interface 行。
// Linux：`ip route get <ip>` 的 dev 字段。
func destOwnerDev(dest string) string {
	if dest == "" {
		return ""
	}
	var out []byte
	if isHostDest(dest) {
		out, _ = exec.Command("route", "-n", "get", hostDestIP(dest)).Output()
	} else {
		_, ipnet, err := net.ParseCIDR(dest)
		if err != nil {
			return ""
		}
		// linux: ip route get；darwin: route get -net
		if ipBin, err := exec.LookPath("ip"); err == nil {
			out, _ = exec.Command(ipBin, "route", "get", ipnet.IP.String()).Output()
			fields := strings.Fields(string(out))
			for i := 0; i < len(fields); i++ {
				if fields[i] == "dev" && i+1 < len(fields) {
					return fields[i+1]
				}
			}
		}
		out, _ = exec.Command("route", "-n", "get", "-net", ipnet.IP.String()).Output()
	}
	if strings.Contains(string(out), "destination: default") {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
		fields := strings.Fields(line)
		for i := 0; i < len(fields); i++ {
			if fields[i] == "dev" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	return ""
}

// routesOnDevice 列出 dev 上的 IPv4 目的地（prefix 形式）。
func routesOnDevice(dev string) []string {
	if ipBin, err := exec.LookPath("ip"); err == nil {
		out, _ := exec.Command(ipBin, "route", "show", "dev", dev).Output()
		var dests []string
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if fields[0] == "local" || fields[0] == "broadcast" || fields[0] == "multicast" {
				continue
			}
			if !strings.Contains(fields[0], "/") && net.ParseIP(fields[0]) == nil {
				continue
			}
			dests = append(dests, fields[0])
		}
		return dests
	}
	// darwin netstat
	out, _ := exec.Command("netstat", "-rn", "-f", "inet").Output()
	var dests []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		for i, f := range fields {
			if f == dev && i >= 1 {
				dest := fields[0]
				if dest == "Destination" || dest == "default" || strings.HasPrefix(dest, "Internet") {
					break
				}
				dests = append(dests, normalizeBSDDest(dest))
				break
			}
		}
	}
	return dests
}

// defaultGateway 读系统默认网关。
func defaultGateway() (gw, dev string) {
	if ipBin, err := exec.LookPath("ip"); err == nil {
		out, err := exec.Command(ipBin, "route", "show", "default").Output()
		if err != nil {
			return "", ""
		}
		fields := strings.Fields(string(out))
		for i := 0; i < len(fields); i++ {
			if fields[i] == "via" && i+1 < len(fields) {
				gw = fields[i+1]
			}
			if fields[i] == "dev" && i+1 < len(fields) {
				dev = fields[i+1]
			}
		}
		return gw, dev
	}
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			gw = strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
		}
		if strings.HasPrefix(line, "interface:") {
			dev = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	return gw, dev
}
