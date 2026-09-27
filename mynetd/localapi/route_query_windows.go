//go:build windows

package localapi

import (
	"net"
	"os/exec"
	"strings"
)

// osRouteExists 查系统路由表里有没有这个目的地。
// Windows 用 `route print -4`（无 per-dest 查询，全文扫 Network Destination）。
func osRouteExists(dest string) bool {
	return windowsRoutePrintHas(dest)
}

// windowsRoutePrintHas 解析 `route print -4`，找目的网络是否在表里。
// 列：Network Destination / Netmask / Gateway / Interface / Metric。
func windowsRoutePrintHas(dest string) bool {
	bin := lookPathAny("route")
	if bin == "" {
		bin = "route"
	}
	out, err := exec.Command(bin, "print", "-4").Output()
	if err != nil {
		return false
	}
	// dest 可能是 10.0.0.1/32 或 10.0.0.1 或 192.168.8.0/24
	wantNet := dest
	wantHost := dest
	if ip, ipnet, err := net.ParseCIDR(dest); err == nil {
		wantHost = ip.String()
		wantNet = ipnet.IP.String()
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		col := f[0]
		if col == "Network" || col == "Active" || col == "Persistent" {
			continue
		}
		if col == wantHost || col == wantNet || strings.HasPrefix(line, wantHost) || strings.HasPrefix(line, wantNet) {
			return true
		}
	}
	return false
}

// localRouteExists 判断目标是否已经由物理网卡承载（非隧道口）。
// Windows：route print 找到该网段且 Interface 不是隧道名。
func localRouteExists(host string) bool {
	bin := lookPathAny("route")
	if bin == "" {
		bin = "route"
	}
	out, err := exec.Command(bin, "print", "-4").Output()
	if err != nil {
		return false
	}
	wantHost := host
	wantNet := host
	if ip, ipnet, err := net.ParseCIDR(host); err == nil {
		wantHost = ip.String()
		wantNet = ipnet.IP.String()
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		col := f[0]
		if col == "Network" || col == "Active" || col == "Persistent" {
			continue
		}
		if col != wantHost && col != wantNet && !strings.HasPrefix(line, wantHost) && !strings.HasPrefix(line, wantNet) {
			continue
		}
		// 最后一列前是 Interface（带 metric 则倒数第二）
		iface := f[len(f)-1]
		if len(f) >= 5 {
			iface = f[len(f)-2]
		}
		l := strings.ToLower(iface)
		return !strings.HasPrefix(l, "ppp") && !strings.Contains(l, "p2p") &&
			!strings.Contains(l, "wintun") && !strings.Contains(l, "gnb") &&
			!strings.Contains(l, "tun")
	}
	return false
}
