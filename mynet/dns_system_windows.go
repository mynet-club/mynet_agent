//go:build windows

package mynet

// Windows 系统 DNS 读写（D4）。
// 用 Get-DnsClientServerAddress / Set-DnsClientServerAddress（PowerShell），
// 或 netsh 回退。servers 空 = ResetServerAddresses（跟随 DHCP）。

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

func runtimeOS() string { return runtime.GOOS }

// listNetworkServices Windows：返回空（由 writeSystemDNS 走默认 iface）。
func listNetworkServices() ([]string, error) {
	return nil, fmt.Errorf("listNetworkServices Windows 未实现")
}

// readSystemDNS Windows：按接口（名或索引）读 DNS。
func readSystemDNS(iface string) ([]string, string, error) {
	idx, err := resolveWindowsIfIndex(iface)
	if err != nil {
		return nil, "", err
	}
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("(Get-DnsClientServerAddress -InterfaceIndex %d -AddressFamily IPv4).ServerAddresses -join ','", idx)).Output()
	if err != nil {
		return nil, fmt.Sprintf("%d", idx), fmt.Errorf("Get-DnsClientServerAddress: %w", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return nil, fmt.Sprintf("%d", idx), nil // DHCP
	}
	return strings.Split(s, ","), fmt.Sprintf("%d", idx), nil
}

// writeSystemDNS Windows：设置或 Reset。
func writeSystemDNS(iface string, servers []string) error {
	idx, err := resolveWindowsIfIndex(iface)
	if err != nil {
		return err
	}
	var cmd string
	if len(servers) == 0 {
		cmd = fmt.Sprintf("Set-DnsClientServerAddress -InterfaceIndex %d -ResetServerAddresses", idx)
	} else {
		list := "'" + strings.Join(servers, "','") + "'"
		cmd = fmt.Sprintf("Set-DnsClientServerAddress -InterfaceIndex %d -ServerAddresses %s", idx, list)
	}
	out, err := exec.Command("powershell", "-NoProfile", "-Command", cmd).CombinedOutput()
	if err != nil {
		return fmt.Errorf("Set-DnsClientServerAddress: %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// resolveWindowsIfIndex iface 可为索引或接口名；空则取第一个 Up 的以太网/Wi-Fi。
func resolveWindowsIfIndex(iface string) (int, error) {
	if iface != "" {
		// 纯数字当索引
		if n, err := strconv.Atoi(iface); err == nil {
			return n, nil
		}
		out, err := exec.Command("powershell", "-NoProfile", "-Command",
			fmt.Sprintf("(Get-NetAdapter -Name '%s').InterfaceIndex", iface)).Output()
		if err != nil {
			return 0, fmt.Errorf("找不到接口 %s: %w", iface, err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			return 0, fmt.Errorf("接口索引解析失败: %q", strings.TrimSpace(string(out)))
		}
		return n, nil
	}
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-NetAdapter | Where-Object {$_.Status -eq 'Up'} | Select-Object -First 1).InterfaceIndex").Output()
	if err != nil {
		return 0, fmt.Errorf("探测接口失败: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n == 0 {
		return 0, fmt.Errorf("找不到 Up 状态的网络接口")
	}
	return n, nil
}
