//go:build windows

package mynet

// Windows 接口计数器：Get-NetAdapterStatistics（按适配器名）。
// 与 darwin netstat、linux sysfs 同源用途 —— node-status 的收/发字节。

import (
	"encoding/json"
	"os/exec"
	"strings"
)

// windowsIfaceBytes 取 Windows 适配器 RX/TX 字节与 MTU。
func windowsIfaceBytes(iface string) (rx, tx int64, mtu int, ok bool) {
	if strings.TrimSpace(iface) == "" {
		return 0, 0, 0, false
	}
	// 单次 PowerShell：统计 + MTU（Get-NetAdapter）
	ps := `$n = ` + psQuote(iface) + `
$s = Get-NetAdapterStatistics -Name $n -ErrorAction SilentlyContinue
$a = Get-NetAdapter -Name $n -ErrorAction SilentlyContinue
if (-not $s) { exit 1 }
@{
  rx = [int64]$s.ReceivedBytes
  tx = [int64]$s.SentBytes
  mtu = if ($a) { [int]$a.Mtu } else { 0 }
} | ConvertTo-Json -Compress`
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		return 0, 0, 0, false
	}
	var raw struct {
		Rx  int64 `json:"rx"`
		Tx  int64 `json:"tx"`
		Mtu int   `json:"mtu"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return 0, 0, 0, false
	}
	return raw.Rx, raw.Tx, raw.Mtu, true
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
