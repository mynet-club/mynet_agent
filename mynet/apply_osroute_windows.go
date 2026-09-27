//go:build windows

package mynet

import (
	"net"
	"os/exec"
	"strings"
)

// destOwnerDev 目的地当前挂在哪块网卡上；查不到返回 ""。
// Windows：解析 `route print -4` 的 Interface 列。
func destOwnerDev(dest string) string {
	if dest == "" {
		return ""
	}
	wantHost := dest
	wantNet := dest
	if ip, ipnet, err := net.ParseCIDR(dest); err == nil {
		wantHost = ip.String()
		wantNet = ipnet.IP.String()
	}
	out, _ := exec.Command("route", "print", "-4").Output()
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		col := f[0]
		if col == "Network" || col == "Active" || col == "Persistent" {
			continue
		}
		if col != wantHost && col != wantNet &&
			!strings.HasPrefix(line, wantHost) && !strings.HasPrefix(line, wantNet) {
			continue
		}
		// Network Destination Netmask Gateway Interface [Metric]
		iface := f[len(f)-1]
		if len(f) >= 5 {
			iface = f[len(f)-2]
		}
		return iface
	}
	return ""
}

// routesOnDevice 列出 dev 上的 IPv4 目的地。Windows：route print 扫 Interface 列。
func routesOnDevice(dev string) []string {
	out, _ := exec.Command("route", "print", "-4").Output()
	var dests []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		col := f[0]
		if col == "Network" || col == "Active" || col == "Persistent" {
			continue
		}
		iface := f[len(f)-1]
		if len(f) >= 5 {
			iface = f[len(f)-2]
		}
		if !strings.EqualFold(iface, dev) {
			continue
		}
		// Network Destination 可能是 192.168.8 这种 BSD 缩写
		dests = append(dests, normalizeBSDDest(col))
	}
	return dests
}

// defaultGateway 读系统默认网关。Windows：route print 的 0.0.0.0 行。
func defaultGateway() (gw, dev string) {
	out, err := exec.Command("route", "print", "-4").Output()
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		if f[0] != "0.0.0.0" && !strings.HasPrefix(line, "0.0.0.0") {
			continue
		}
		// 0.0.0.0 0.0.0.0 <gw> <iface> <metric>
		gw = f[2]
		dev = f[len(f)-2]
		if len(f) >= 5 {
			dev = f[3]
		}
		return gw, dev
	}
	return "", ""
}
