//go:build linux

package mynet

import (
	"fmt"
	"os"
)

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
	m.System.CPUPercent = linuxCPUPercent()
}

// collectPlatformSystemMetrics linux 平台系统指标。
func collectPlatformSystemMetrics(m *Metrics) { collectLinuxSystemMetrics(m) }
