package mynet

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// 一次读取 FIB，供一轮对账复用，避免每个 IP 都起 route 子进程。
type pcRouteView map[string]string // prefix → interface

func readPCRoutes() (pcRouteView, error) {
	if runtime.GOOS == "linux" {
		out, err := exec.Command("ip", "-j", "-4", "route", "show", "table", "main").Output()
		if err != nil {
			return nil, err
		}
		return parseLinuxPCRoutes(out)
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
			"ConvertTo-Json -Compress -InputObject @(Get-NetRoute -AddressFamily IPv4 -PolicyStore ActiveStore | Sort-Object { $_.RouteMetric + $_.InterfaceMetric } | Select-Object DestinationPrefix,InterfaceIndex)").Output()
		if err != nil {
			return nil, err
		}
		var rows []struct {
			DestinationPrefix string
			InterfaceIndex    int
		}
		if err := json.Unmarshal(out, &rows); err != nil {
			return nil, err
		}
		view := pcRouteView{}
		for _, row := range rows {
			if _, exists := view[row.DestinationPrefix]; !exists {
				view[row.DestinationPrefix] = strconv.Itoa(row.InterfaceIndex)
			}
		}
		return view, nil
	}
	out, err := exec.Command("netstat", "-rn", "-f", "inet").Output()
	if err != nil {
		return nil, err
	}
	return parsePCNetstat(string(out))
}

func parsePCNetstat(out string) (pcRouteView, error) {
	view := pcRouteView{}
	ifaceCol := -1
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "Destination" {
			for i, field := range fields {
				if field == "Netif" || field == "Iface" {
					ifaceCol = i
				}
			}
			continue
		}
		if ifaceCol < 0 || len(fields) <= ifaceCol {
			continue
		}
		if runtime.GOOS == "darwin" && len(fields) > 2 && strings.Contains(fields[2], "I") {
			continue
		} // macOS 接口作用域路由不参与普通目的选路
		dest := fields[0]
		if dest == "default" {
			dest = "0.0.0.0/0"
		}
		parts := strings.SplitN(dest, "/", 2)
		octets := strings.Split(parts[0], ".")
		bits := len(octets) * 8
		for len(octets) < 4 {
			octets = append(octets, "0")
		}
		if len(parts) > 1 {
			dest = strings.Join(octets, ".") + "/" + parts[1]
		} else {
			dest = fmt.Sprintf("%s/%d", strings.Join(octets, "."), bits)
		}
		_, network, err := net.ParseCIDR(dest)
		if err == nil {
			view[network.String()] = fields[ifaceCol]
		}
	}
	if ifaceCol < 0 {
		return nil, fmt.Errorf("netstat route table has no interface column")
	}
	return view, nil
}

func (v pcRouteView) routed(ip, tun string) bool {
	target := net.ParseIP(ip).To4()
	if target == nil || tun == "" {
		return false
	}
	addr := binary.BigEndian.Uint32(target)
	// FIB 最长前缀匹配最多 33 次 map 查找，不对每个 IP 重解析整张路由表。
	for bits := 32; bits >= 0; bits-- {
		mask := ^uint32(0) << uint(32-bits)
		prefix := uint32ToIP(addr&mask) + "/" + strconv.Itoa(bits)
		if dev, ok := v[prefix]; ok {
			return dev == tun
		}
	}
	return false
}

func (v pcRouteView) covers(prefix, tun string) bool {
	ip, network, err := net.ParseCIDR(prefix)
	if err != nil || ip.To4() == nil {
		return false
	}
	// MergeIPsToCIDR 只产生 /24.. /32，检查整个精确集合，不拿首个 IP 代表整段。
	for addr := ip.To4(); network.Contains(addr); {
		if !v.routed(addr.String(), tun) {
			return false
		}
		carry := true
		for i := len(addr) - 1; i >= 0 && carry; i-- {
			addr[i]++
			carry = addr[i] == 0
		}
		if carry {
			break
		}
	}
	return true
}

func parseLinuxPCRoutes(raw []byte) (pcRouteView, error) {
	var rows []struct {
		Dst    string `json:"dst"`
		Dev    string `json:"dev"`
		Metric int    `json:"metric"`
		Type   string `json:"type"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	view := pcRouteView{}
	metrics := map[string]int{}
	for _, row := range rows {
		dest := row.Dst
		if dest == "default" {
			dest = "0.0.0.0/0"
		}
		if !strings.Contains(dest, "/") {
			dest += "/32"
		}
		_, network, err := net.ParseCIDR(dest)
		if err != nil {
			return nil, fmt.Errorf("invalid ip route destination %q", row.Dst)
		}
		dest = network.String()
		if prev, ok := metrics[dest]; ok && prev <= row.Metric {
			continue
		}
		metrics[dest] = row.Metric
		// blackhole/unreachable 仍参与最长前缀匹配，不能被更宽隧道路由误判为可达。
		view[dest] = row.Dev
	}
	return view, nil
}
