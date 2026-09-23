package mynet

import (
	"fmt"
	"os"
	"runtime"
)

// collectSystemMetrics 系统指标。实现按平台分支；暂用 /proc 与 sysctl 兜底，
// 后续可换 gopsutil（仍在 mynet，不在 mynetd）。
func collectSystemMetrics(m *Metrics) {
	m.Hostname, _ = os.Hostname()
	switch runtime.GOOS {
	case "linux":
		collectLinuxSystemMetrics(m)
	case "darwin":
		collectDarwinSystemMetrics(m)
	default:
		m.Notes = append(m.Notes, "system metrics 未覆盖 "+runtime.GOOS)
	}
}

func collectLinuxSystemMetrics(m *Metrics) {
	// loadavg
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		var l1 float64
		if n, _ := fmt.Sscanf(string(b), "%f", &l1); n == 1 {
			m.System.Load1 = l1
		}
	}
	// uptime
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		var up float64
		if n, _ := fmt.Sscanf(string(b), "%f", &up); n == 1 {
			m.System.UptimeSec = int64(up)
		}
	}
	// meminfo
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, avail uint64
		for _, line := range splitLines(string(b)) {
			var v uint64
			if n, _ := fmt.Sscanf(line, "MemTotal: %d kB", &v); n == 1 {
				total = v / 1024
			}
			if n, _ := fmt.Sscanf(line, "MemAvailable: %d kB", &v); n == 1 {
				avail = v / 1024
			}
		}
		m.System.MemTotalMB = total
		m.System.MemUsedMB = total - avail
	}
}

func collectDarwinSystemMetrics(m *Metrics) {
	m.Notes = append(m.Notes, "darwin metrics 为精简实现")
	if b, err := os.ReadFile("/var/run/system_info"); err == nil {
		_ = b
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// collectVPNMetrics 从各驱动只读接口收集（wg show / gnb_ctl -s）。
// 解析也只在 mynet，不进 mynetd。
func collectVPNMetrics(m *Metrics) {
	if ms := probeWireGuard(); len(ms) > 0 {
		m.VPN = append(m.VPN, ms...)
	}
	if ms := probeGNB(); len(ms) > 0 {
		m.VPN = append(m.VPN, ms...)
	}
	if len(m.VPN) == 0 {
		m.Notes = append(m.Notes, "未发现活动 VPN 驱动")
	}
}
