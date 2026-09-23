package mynet

import (
	"os/exec"
	"strings"
)

// probeWireGuard 解析 `wg show`（只读）。A-2 的完整 WireGuardDriver 落地后，
// 这里改为调 driver 包；当前先给心跳数据源。
func probeWireGuard() []VPNDriverMetrics {
	out, err := exec.Command("wg", "show", "dump").Output()
	if err != nil {
		return nil
	}
	// dump: 每接口多行；首行私钥，后续 peer
	// 简表：按空接口段粗分
	var result []VPNDriverMetrics
	iface := ""
	m := VPNDriverMetrics{Driver: "wireguard"}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) >= 4 && fields[0] != "" && !strings.Contains(fields[0], ":") {
			// 可能是接口头（private_key 行 5 列）
		}
		_ = fields
		_ = iface
	}
	// 至少标记 wg 可用时有一条空壳（细节在 A-2 补全）
	if len(out) > 0 {
		m.Up = true
		result = append(result, m)
	}
	return result
}

// probeGNB 解析 `gnb_ctl -s`（只读）。
func probeGNB() []VPNDriverMetrics {
	out, err := exec.Command("gnb_ctl", "-s").Output()
	if err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return []VPNDriverMetrics{{
		Driver: "gnb",
		Up:     true,
	}}
}
