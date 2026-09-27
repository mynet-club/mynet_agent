//go:build darwin

package mynet

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestDarwinSystemMetricsMatchSysctl(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin 专用：本机是 " + runtime.GOOS + "，sysctl/vm_stat/top 的口径不同")
	}
	m := &Metrics{}
	collectDarwinSystemMetrics(m)

	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		t.Fatalf("sysctl hw.memsize: %v", err)
	}
	sz, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Fatalf("解析 hw.memsize: %v", err)
	}
	if want := sz / 1024 / 1024; uint64(m.System.MemTotalMB) != want {
		t.Errorf("MemTotalMB = %d，而 sysctl hw.memsize 说 %d", m.System.MemTotalMB, want)
	}
	if m.System.MemUsedMB == 0 || m.System.MemUsedMB > m.System.MemTotalMB {
		t.Errorf("MemUsedMB = %d 不合理（应在 0 与总量 %d 之间）", m.System.MemUsedMB, m.System.MemTotalMB)
	}
	if m.System.UptimeSec <= 0 {
		t.Errorf("UptimeSec = %d，期望 > 0", m.System.UptimeSec)
	}
	if m.System.Load1 < 0 {
		t.Errorf("Load1 = %v 为负", m.System.Load1)
	}
	// CPUPercent 与 Load1 都**允许**为 0（机器真的空闲时就是 0），
	// 所以不断言非 0 —— 断言一个可能合法为 0 的值非 0，是会让测试随机红的。
}

func TestDarwinMemUsedNonZero(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin 专用")
	}
	if v := darwinMemUsedMB(); v == 0 {
		t.Fatal("darwinMemUsedMB() = 0 —— 页大小没读到，或 vm_stat 输出格式变了")
	}
}
