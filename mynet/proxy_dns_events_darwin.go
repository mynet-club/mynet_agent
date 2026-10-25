//go:build darwin

package mynet

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
)

// Dynamic Store 通知覆盖 Wi-Fi 重连、服务增删与 VPN 改写；定时检查为兜底。
func hookDNSChangeEvents(ctx context.Context) <-chan struct{} {
	events := make(chan struct{}, 1)
	cmd := exec.CommandContext(ctx, "scutil")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil
	}
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		return nil
	}
	if _, err := io.WriteString(in, "n.add State:/Network/Global/DNS\nn.add State:/Network/Service/.*/DNS pattern\nn.add Setup:/Network/Service/.*/DNS pattern\nn.watch\n"); err != nil {
		_ = in.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil
	}
	go func() {
		defer in.Close()
		scan := bufio.NewScanner(out)
		for scan.Scan() {
			select {
			case events <- struct{}{}:
			default:
			}
		}
		_ = cmd.Wait()
	}()
	return events
}

func effectiveHookDNSServers() ([]string, error) {
	out, err := exec.Command("scutil", "--dns").Output()
	if err != nil {
		return nil, err
	}
	return parseMacPrimaryDNS(string(out))
}
func parseMacPrimaryDNS(text string) ([]string, error) {
	// 域名专用 resolver 不替代默认解析器；只核对普通系统 DNS 路径。
	for _, block := range strings.Split(text, "resolver #") {
		var servers []string
		supplemental := false
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "domain ") && !strings.HasSuffix(line, ": .") {
				supplemental = true
			}
			if strings.Contains(line, "Supplemental") || strings.Contains(line, "options  : mdns") {
				supplemental = true
			}
			if strings.HasPrefix(line, "nameserver[") {
				if i := strings.Index(line, " : "); i >= 0 {
					servers = append(servers, strings.TrimSpace(line[i+3:]))
				}
			}
		}
		if !supplemental && len(servers) > 0 {
			return servers, nil
		}
	}
	return nil, fmt.Errorf("effective DNS resolver unavailable")
}

// 只核对实际联网的服务；未连接的 Wi-Fi 或备用网卡不决定当前加速状态。
func hookSystemDNSOps() systemDNSOps {
	ops := nativeSystemDNSOps()
	ops.list = func() ([]string, error) {
		active := map[string]bool{}
		ifaces, err := net.Interfaces()
		if err != nil {
			return nil, err
		}
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				return nil, err
			}
			for _, addr := range addrs {
				ip, _, err := net.ParseCIDR(addr.String())
				if err == nil && !ip.IsLinkLocalUnicast() && !ip.IsLoopback() {
					active[iface.Name] = true
				}
			}
		}
		raw, err := exec.Command("networksetup", "-listnetworkserviceorder").Output()
		if err != nil {
			return nil, err
		}
		return parseMacActiveDNSServices(string(raw), active), nil
	}
	return ops
}
func parseMacActiveDNSServices(text string, active map[string]bool) []string {
	var names []string
	service := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, ") "); strings.HasPrefix(line, "(") && i > 0 {
			service = line[i+2:]
			if strings.Contains(line[:i], "*") {
				service = ""
			}
			continue
		}
		if i := strings.Index(line, "Device: "); i >= 0 && service != "" {
			dev := strings.TrimSpace(strings.TrimSuffix(line[i+len("Device: "):], ")"))
			if active[dev] {
				names = append(names, service)
			}
		}
	}
	return names
}
