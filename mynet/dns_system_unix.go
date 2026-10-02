//go:build !windows

package mynet

// Unix（Linux/macOS）系统 DNS 读写。
// macOS 用 networksetup（按网络服务名）；Linux 写 /etc/resolv.conf。

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func runtimeOS() string { return runtime.GOOS }

// readSystemDNS 返回 (servers, iface, err)。
// macOS：networksetup -getdnsservers <service>（"There aren't any…" = 跟随 DHCP → 空列表）。
// Linux：解析 /etc/resolv.conf 的 nameserver。
func readSystemDNS(iface string) ([]string, string, error) {
	switch runtime.GOOS {
	case "darwin":
		svc, err := resolveMacDNSService(iface)
		if err != nil {
			return nil, "", err
		}
		out, err := exec.Command("networksetup", "-getdnsservers", svc).Output()
		if err != nil {
			return nil, svc, fmt.Errorf("networksetup -getdnsservers: %w", err)
		}
		s := strings.TrimSpace(string(out))
		if s == "" || strings.Contains(s, "There aren't any") {
			return nil, svc, nil // DHCP
		}
		var servers []string
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				servers = append(servers, line)
			}
		}
		return servers, svc, nil
	default:
		// Linux / 其它
		b, _ := os.ReadFile("/etc/resolv.conf")
		var servers []string
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "nameserver ") {
				servers = append(servers, strings.TrimSpace(strings.TrimPrefix(line, "nameserver ")))
			}
		}
		return servers, iface, nil
	}
}

// writeSystemDNS 设置系统 DNS。servers 为空 = 恢复跟随 DHCP（macOS empty / Linux 不写）。
func writeSystemDNS(iface string, servers []string) error {
	switch runtime.GOOS {
	case "darwin":
		svc, err := resolveMacDNSService(iface)
		if err != nil {
			return err
		}
		args := []string{"-setdnsservers", svc}
		if len(servers) == 0 {
			args = append(args, "empty") // 跟随 DHCP
		} else {
			// ⚠️ networksetup 只认 IP，不认域名（实测：dns.mynet.club is not a valid IP）
			// 域名先解析成 IP 再写
			for _, s := range servers {
				ip := resolveDNSHostToIP(s)
				if ip == "" {
					return fmt.Errorf("DNS 主机 %q 解析不出 IP，无法写入 networksetup", s)
				}
				args = append(args, ip)
			}
		}
		out, err := exec.Command("networksetup", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("networksetup -setdnsservers: %v %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	default:
		// Linux：只在接管时写 127.0.0.1；还原时不覆盖（可能被 NetworkManager 管）
		if len(servers) == 0 {
			return nil
		}
		var b strings.Builder
		b.WriteString("# managed by mynet proxy — restore on stop\n")
		for _, s := range servers {
			b.WriteString("nameserver " + s + "\n")
		}
		return os.WriteFile("/etc/resolv.conf", []byte(b.String()), 0o644)
	}
}

// listNetworkServices 列出所有启用的网络服务（macOS networksetup）。
func listNetworkServices() ([]string, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("listNetworkServices 仅 macOS")
	}
	out, err := exec.Command("networksetup", "-listallnetworkservices").Output()
	if err != nil {
		return nil, err
	}
	var res []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "An asterisk") {
			continue
		}
		if strings.HasPrefix(line, "*") {
			continue // disabled
		}
		res = append(res, line)
	}
	return res, nil
}

// resolveMacDNSService 确定要改哪个网络服务。
// 优先用调用方给的；否则取第一个 enabled 服务里带 Wi-Fi/Ethernet 的。
func resolveMacDNSService(iface string) (string, error) {
	if iface != "" {
		return iface, nil
	}
	out, err := exec.Command("networksetup", "-listallnetworkservices").Output()
	if err != nil {
		return "", fmt.Errorf("networksetup -listallnetworkservices: %w", err)
	}
	var fallback string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "An asterisk") {
			continue
		}
		// 开头 * 表示 disabled
		if strings.HasPrefix(line, "*") {
			continue
		}
		if fallback == "" {
			fallback = line
		}
		low := strings.ToLower(line)
		if strings.Contains(low, "wi-fi") || strings.Contains(low, "wifi") ||
			strings.Contains(low, "ethernet") || strings.Contains(low, "usb") {
			return line, nil
		}
	}
	if fallback == "" {
		return "", fmt.Errorf("找不到可用的 macOS 网络服务")
	}
	return fallback, nil
}
