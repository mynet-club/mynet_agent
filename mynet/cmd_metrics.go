package mynet

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Metrics 是心跳数据源：系统指标 + VPN/peer 状态。
// mynetd 取 `mynet metrics --json` 的 stdout 组装心跳，自身不做 gopsutil。
type Metrics struct {
	CollectedAt time.Time          `json:"collected_at"`
	Hostname    string             `json:"hostname,omitempty"`
	System      SystemMetrics      `json:"system"`
	VPN         []VPNDriverMetrics `json:"vpn,omitempty"`
	Notes       []string           `json:"notes,omitempty"`
}

type SystemMetrics struct {
	CPUPercent  float64 `json:"cpu_percent,omitempty"`
	MemUsedMB   uint64  `json:"mem_used_mb,omitempty"`
	MemTotalMB  uint64  `json:"mem_total_mb,omitempty"`
	Load1       float64 `json:"load1,omitempty"`
	UptimeSec   int64   `json:"uptime_sec,omitempty"`
	InterfaceUp int     `json:"interface_up,omitempty"`
	// DiskUsedPercent 根文件系统已用百分比（0-100）。
	// 后端的心跳契约把 disk_usage 列为**必填**（HeartbeatRequest.php:66），
	// 而此前 metrics 根本没采磁盘 —— 少一个字段整条心跳就过不了验证。
	DiskUsedPercent float64 `json:"disk_used_percent,omitempty"`
}

// VPNDriverMetrics 驱动侧状态。字段名不含驱动原生术语（AllowedIPs 等），
// 只暴露统一视图；driver= 标识驱动类型（与 Envelope 一致，允许）。
type VPNDriverMetrics struct {
	Driver    string   `json:"driver"` // gnb | wireguard
	Up        bool     `json:"up"`
	Interface string   `json:"interface,omitempty"`
	Peers     int      `json:"peers,omitempty"`
	PeersUp   int      `json:"peers_up,omitempty"`
	Addrs     []string `json:"addrs,omitempty"`
	// RxBytes / TxBytes 是该驱动下**所有 peer 的累计流量**。
	// 后端的心跳契约把 node_self.rx_bytes / tx_bytes 列为必填
	// （HeartbeatRequest.php:104-105），而 driver 层本来就解析了每个 peer 的
	// 收发字节 —— 早先投影成统一视图时把它们丢了，等于白解析。
	RxBytes uint64 `json:"rx_bytes,omitempty"`
	TxBytes uint64 `json:"tx_bytes,omitempty"`
}

// cmdMetrics — S2-1。用法: mynet metrics [--json]
func cmdMetrics(args []string) int {
	asJSON := false
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		}
	}

	m := &Metrics{CollectedAt: time.Now().UTC()}
	collectSystemMetrics(m)
	collectVPNMetrics(m)

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(m); err != nil {
			fmt.Fprintf(os.Stderr, "encode metrics: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Printf("collected_at=%s cpu=%.1f%% mem=%d/%dMB load1=%.2f vpn=%d\n",
		m.CollectedAt.Format(time.RFC3339),
		m.System.CPUPercent, m.System.MemUsedMB, m.System.MemTotalMB,
		m.System.Load1, len(m.VPN))
	return 0
}
