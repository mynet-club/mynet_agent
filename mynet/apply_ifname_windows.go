//go:build windows

package mynet

import (
	"net"
	"strconv"
	"strings"
)

// platformIfaceToken Windows netsh 的 interface= 吃**名称或 ifIndex**；
// 用 index 更稳（名称含中文/空格时易踩语法错）。
func platformIfaceToken(name string) string {
	if ni, err := net.InterfaceByName(name); err == nil {
		return strconv.Itoa(ni.Index)
	}
	return name
}

// platformTunnelIface wintun 默认名 P2PNet 或任意隧道口的 ifIndex。
func platformTunnelIface(name string) string {
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			n := iface.Name
			if n == "P2PNet" || strings.Contains(strings.ToLower(n), "wintun") ||
				strings.Contains(strings.ToLower(n), "gnb") {
				return strconv.Itoa(iface.Index)
			}
		}
	}
	return name
}
