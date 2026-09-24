package mynet

import (
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// collectSystemMetrics 系统指标。实现按平台分支。
//
// ⚠️ 这些采集**只在 mynet（执行器）里**，不进 mynetd —— AGENTS.md §1.2：
// mynetd 是纯通信层，探测/解析一律归执行器。
func collectSystemMetrics(m *Metrics) {
	m.Hostname, _ = os.Hostname()
	// InterfaceUp 跨平台，用标准库数，不必去解析 ip / ifconfig 的文本输出
	m.System.InterfaceUp = countUpInterfaces()
	switch runtime.GOOS {
	case "linux":
		collectLinuxSystemMetrics(m)
	case "darwin":
		collectDarwinSystemMetrics(m)
	default:
		m.Notes = append(m.Notes, "system metrics 未覆盖 "+runtime.GOOS)
	}
}

// countUpInterfaces 数 up 状态的非 loopback 接口。
// 排除 loopback：它恒 up，算进去这个指标就永远「1 起步」，失去意义。
func countUpInterfaces() int {
	ifs, err := net.Interfaces()
	if err != nil {
		return 0
	}
	n := 0
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		n++
	}
	return n
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
	m.System.CPUPercent = linuxCPUPercent()
}

// linuxCPUPercent 读两次 /proc/stat 求差。
//
// 单次读没有意义 —— 那里的 jiffies 是**自开机累计值**，必须两次采样才能得到
// 「这段时间的占用率」。间隔 200ms：再短噪声大，再长拖慢命令。
func linuxCPUPercent() float64 {
	a, ok := readCPUJiffies()
	if !ok {
		return 0
	}
	time.Sleep(200 * time.Millisecond)
	b, ok := readCPUJiffies()
	if !ok {
		return 0
	}
	if b.total <= a.total {
		return 0
	}
	total := b.total - a.total
	idle := b.idle - a.idle
	if idle > total {
		return 0
	}
	return round2(float64(total-idle) * 100 / float64(total))
}

type cpuJiffies struct{ total, idle uint64 }

// readCPUJiffies 读 /proc/stat 的汇总行 "cpu  user nice system idle iowait irq softirq steal …"。
// idle 口径 = idle + iowait（第 4、5 个字段），与 top 的算法一致。
func readCPUJiffies() (cpuJiffies, bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuJiffies{}, false
	}
	for _, line := range splitLines(string(b)) {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		f := strings.Fields(line)
		var j cpuJiffies
		for i, s := range f[1:] {
			v, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				continue
			}
			j.total += v
			if i == 3 || i == 4 { // idle, iowait
				j.idle += v
			}
		}
		return j, true
	}
	return cpuJiffies{}, false
}

// collectDarwinSystemMetrics 用 sysctl / vm_stat / top 真读。
//
// 早先这里是空壳：读一个**根本不存在的** `/var/run/system_info`，然后 `_ = b`
// 把内容丢掉，只留一句「darwin metrics 为精简实现」的 note。于是 macOS 上
// `mynet metrics` 的系统段恒为全 0 —— 而开发机恰好就是 macOS，
// 所以这个「全 0」一直摆在眼前却没人当回事。
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

// darwinMemUsedMB = (active + wired + 压缩器占用) × page size，数据来自 vm_stat。
// 口径对齐「活动监视器」的已用内存：不含 free / inactive / speculative。
// 注意用 `Pages occupied by compressor`（实际占的物理页），
// 不是 `Pages stored in compressor`（被压缩的逻辑页数，会大好几倍）。
func darwinMemUsedMB() uint64 {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0
	}
	text := string(out)
	pageSize := uint64(0)
	if i := strings.Index(text, "page size of "); i >= 0 {
		pageSize = leadingUint(text[i+len("page size of "):])
	}
	if pageSize == 0 {
		return 0 // 拿不到页大小就不要瞎算，宁可报 0
	}
	var active, wired, compressed uint64
	for _, line := range splitLines(text) {
		v := uintAfterColon(line)
		switch {
		case strings.HasPrefix(line, "Pages active:"):
			active = v
		case strings.HasPrefix(line, "Pages wired down:"):
			wired = v
		case strings.HasPrefix(line, "Pages occupied by compressor:"):
			compressed = v
		}
	}
	return (active + wired + compressed) * pageSize / 1024 / 1024
}

// darwinLoad1 解析 `sysctl -n vm.loadavg` → "{ 3.73 4.74 5.24 }"，取第一个。
func darwinLoad1() float64 {
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return 0
	}
	f := strings.Fields(strings.Trim(string(out), "{} \n"))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// darwinUptimeSec = now - kern.boottime 的 sec 字段。
// 输出形如 "{ sec = 1790079305, usec = 142118 } Tue Sep 22 20:15:05 2026"。
func darwinUptimeSec() int64 {
	out, err := exec.Command("sysctl", "-n", "kern.boottime").Output()
	if err != nil {
		return 0
	}
	s := string(out)
	i := strings.Index(s, "sec = ")
	if i < 0 {
		return 0
	}
	rest := s[i+len("sec = "):]
	if j := strings.IndexAny(rest, ",} "); j >= 0 {
		rest = rest[:j]
	}
	sec, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0
	}
	if up := time.Now().Unix() - sec; up > 0 {
		return up
	}
	return 0
}

// darwinCPUPercent 用 `top -l 2 -n 0 -s 0` 取**第二次**采样。
//
// 为什么是第二次：top 第一行 CPU usage 是自开机以来的平均，不是当前值 ——
// 取第一次会得到一个几乎不动的数。
// 为什么不用 `ps -A -o %cpu` 求和：macOS 的 ps %cpu 是**进程生命周期内的平均**，
// 长期运行的进程会把值拉平，测不出当前负载。
// 代价：这条命令约 1 秒（`-s 0` 已是能拿到的最快间隔，实测 1.03s）。
// 心跳间隔通常 30s 以上，占比可接受；将来若要更快，得改用 Mach 的
// host_statistics（需引入 golang.org/x/sys），本次没有为了省这一秒加依赖。
func darwinCPUPercent() float64 {
	out, err := exec.Command("top", "-l", "2", "-n", "0", "-s", "0").Output()
	if err != nil {
		return 0
	}
	var last string
	for _, line := range splitLines(string(out)) {
		if strings.HasPrefix(line, "CPU usage:") {
			last = line // 取最后一次
		}
	}
	if last == "" {
		return 0
	}
	var user, sys float64
	for _, part := range strings.Split(strings.TrimPrefix(last, "CPU usage:"), ",") {
		f := strings.Fields(part)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSuffix(f[0], "%"), 64)
		if err != nil {
			continue
		}
		switch f[1] {
		case "user":
			user = v
		case "sys":
			sys = v
		}
	}
	return round2(user + sys)
}

// round2 保留两位小数。不 round 的话浮点加法会留下
// `27.840000000000003` 这种尾巴，而它会原样进 JSON、进心跳、进 back 的 Redis。
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// uintAfterColon 取冒号后的第一段数字（"Pages active:   257583." → 257583）。
func uintAfterColon(line string) uint64 {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return 0
	}
	return leadingUint(strings.TrimSpace(line[i+1:]))
}

// leadingUint 取开头那段连续数字，遇到非数字即停。
func leadingUint(s string) uint64 {
	j := 0
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == 0 {
		return 0
	}
	v, _ := strconv.ParseUint(s[:j], 10, 64)
	return v
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
