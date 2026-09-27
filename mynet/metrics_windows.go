//go:build windows

package mynet

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// collectWindowsSystemMetrics 用 CIM（WMI）真读，不再留 0。
//
// 一次 PowerShell 取齐内存/开机时间/磁盘，CPU 单独取（LoadPercentage
// 在多插槽机上是 per-socket，且经常为 null；用性能计数器的
// \Processor(_Total)\% Processor Time 更稳，代价是约 1 秒采样）。
func collectWindowsSystemMetrics(m *Metrics) {
	ps := `$os = Get-CimInstance Win32_OperatingSystem
$disk = Get-CimInstance Win32_LogicalDisk -Filter ("DeviceID='" + $env:SystemDrive + "'")
@{
  mem_total_kb = [int64]$os.TotalVisibleMemorySize
  free_kb      = [int64]$os.FreePhysicalMemory
  boot         = $os.LastBootUpTime.ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
  disk_size    = [int64]$disk.Size
  disk_free    = [int64]$disk.FreeSpace
} | ConvertTo-Json -Compress`
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		m.Notes = append(m.Notes, "windows metrics CIM 失败: "+err.Error())
		return
	}
	var raw struct {
		MemTotalKB int64  `json:"mem_total_kb"`
		FreeKB     int64  `json:"free_kb"`
		Boot       string `json:"boot"`
		DiskSize   int64  `json:"disk_size"`
		DiskFree   int64  `json:"disk_free"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		m.Notes = append(m.Notes, "windows metrics JSON 解析失败: "+err.Error())
		return
	}
	if raw.MemTotalKB > 0 {
		m.System.MemTotalMB = uint64(raw.MemTotalKB / 1024)
		m.System.MemUsedMB = uint64((raw.MemTotalKB - raw.FreeKB) / 1024)
	}
	if t, err := time.Parse(time.RFC3339, raw.Boot); err == nil {
		if up := int64(time.Since(t).Seconds()); up > 0 {
			m.System.UptimeSec = up
		}
	}
	if raw.DiskSize > 0 {
		m.System.DiskUsedPercent = round2(float64(raw.DiskSize-raw.DiskFree) * 100 / float64(raw.DiskSize))
	}
	m.System.CPUPercent = windowsCPUPercent()
	// Windows 没有 loadavg；留 0 并说明，不造假数
	m.Notes = append(m.Notes, "windows 无 loadavg，load1 恒 0")
}

// windowsDiskUsedPercent 只取系统盘已用百分比（无 path 语义）。
func windowsDiskUsedPercent() float64 {
	ps := `$d = Get-CimInstance Win32_LogicalDisk -Filter ("DeviceID='" + $env:SystemDrive + "'")
if ($d.Size -gt 0) { [math]::Round(($d.Size - $d.FreeSpace) * 100.0 / $d.Size, 2) } else { 0 }`
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return round2(v)
}

// windowsCPUPercent 用性能计数器采一次（约 1 秒）。
// Win32_Processor.LoadPercentage 是瞬时值且经常为 null，不可靠。
func windowsCPUPercent() float64 {
	ps := `(Get-Counter '\Processor(_Total)\% Processor Time' -SampleInterval 1 -MaxSamples 1).CounterSamples.CookedValue`
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0
	}
	return round2(v)
}

// diskUsedPercentOS Windows 走 CIM；unix 返回 -1 表示交回 df。
func diskUsedPercentOS(path string) float64 {
	_ = path
	return windowsDiskUsedPercent()
}

// collectPlatformSystemMetrics windows 平台系统指标。
func collectPlatformSystemMetrics(m *Metrics) { collectWindowsSystemMetrics(m) }
