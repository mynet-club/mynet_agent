//go:build darwin

package mynet

import (
	"os/exec"
	"strconv"
	"strings"
)

func collectDarwinSystemMetrics(m *Metrics) {
	if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
		if bytes, e := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64); e == nil {
			m.System.MemTotalMB = bytes / 1024 / 1024
		}
	}
	m.System.MemUsedMB = darwinMemUsedMB()
	m.System.Load1 = darwinLoad1()
	m.System.UptimeSec = darwinUptimeSec()
	m.System.CPUPercent = darwinCPUPercent()
}

// collectPlatformSystemMetrics darwin 平台系统指标。
func collectPlatformSystemMetrics(m *Metrics) { collectDarwinSystemMetrics(m) }
