package domain

import (
	"fmt"
	"time"
)

// VPNAddress VPN 地址信息
type VPNAddress struct {
	Address     string    `json:"address"`      // IP:Port 或 [IPv6]:Port
	Index       int       `json:"index"`        // 地址索引
	LatencyUs   int64     `json:"latency_us"`   // 延迟(微秒)
	LastSeen    time.Time `json:"last_seen"`    // 最后通信时间
	AddressType string    `json:"address_type"` // static/dynamic/available/push/resolv
}

// VPNPeer VPN对等节点实体 (Driver无关的抽象)
// 适用于 GNB、WireGuard 等任何 VPN driver
type VPNPeer struct {
	// 基础信息
	PeerID     int64  `json:"peer_id"`     // ✅ 对等节点ID (必须int64)
	NodeID     int64  `json:"node_id"`     // ✅ 关联的MyNet节点ID
	VirtualIP  string `json:"virtual_ip"`  // 虚拟网络IP (10.x.x.x)
	VirtualIP6 string `json:"virtual_ip6"` // 虚拟网络IPv6

	// 连接状态
	ConnectionStatus string `json:"connection_status"` // online, offline, relay
	ConnectionType   string `json:"connection_type"`   // direct_ipv4, direct_ipv6, dual_stack, relay
	IsLocal          bool   `json:"is_local"`          // 是否本地节点

	// 网络指标
	LatencyMs  float64 `json:"latency_ms"`  // 延迟(毫秒)
	PacketLoss float64 `json:"packet_loss"` // 丢包率(0-100)
	Jitter     float64 `json:"jitter"`      // 抖动(ms)
	RxBytes    uint64  `json:"rx_bytes"`    // 接收字节
	TxBytes    uint64  `json:"tx_bytes"`    // 发送字节
	RxPackets  uint64  `json:"rx_packets"`  // 接收包数
	TxPackets  uint64  `json:"tx_packets"`  // 发送包数

	// 质量评估
	QualityScore int    `json:"quality_score"` // 连接质量(0-100)
	QualityGrade string `json:"quality_grade"` // A/B/C/D

	// 时间戳
	LastSeen      time.Time `json:"last_seen"`      // 最后通信时间
	LastHandshake time.Time `json:"last_handshake"` // 最后握手时间
	ConnectedAt   time.Time `json:"connected_at"`   // 连接建立时间

	// 端点信息
	Endpoint     string    `json:"endpoint"`       // 当前使用的端点 (IP:Port)
	WANIPv4      string    `json:"wan_ipv4"`       // WAN IPv4地址
	WANIPv6      string    `json:"wan_ipv6"`       // WAN IPv6地址
	AllowedIPs   string    `json:"allowed_ips"`    // 允许的IP范围
	PersistentKA int       `json:"persistent_ka"`  // 保活间隔(秒)
	LastActiveAt time.Time `json:"last_active_at"` // 最后活动时间

	// 地址列表
	AvailableAddresses4 []VPNAddress `json:"available_addresses4"` // 可用IPv4地址
	AvailableAddresses6 []VPNAddress `json:"available_addresses6"` // 可用IPv6地址
	StaticAddresses     []VPNAddress `json:"static_addresses"`     // 静态地址列表
	DynamicAddresses    []VPNAddress `json:"dynamic_addresses"`    // 动态地址列表
	PushAddresses       []VPNAddress `json:"push_addresses"`       // Push地址列表
	ResolvAddresses     []VPNAddress `json:"resolv_addresses"`     // 解析地址列表

	// GNB 原始字段
	DetectCount      uint32  `json:"detect_count"`        // 检测失败计数
	Addr4LatencyUs   int64   `json:"addr4_latency_us"`    // IPv4延迟(微秒)
	Addr6LatencyUs   int64   `json:"addr6_latency_us"`    // IPv6延迟(微秒)
	Addr4UpdateTsSec int64   `json:"addr4_update_ts_sec"` // IPv4地址最后更新时间戳(秒)
	Addr6UpdateTsSec int64   `json:"addr6_update_ts_sec"` // IPv6地址最后更新时间戳(秒)
	Addr4IP          string  `json:"addr4_ip"`            // 最新 IPv4 地址（不含端口）
	Addr6IP          string  `json:"addr6_ip"`            // 最新 IPv6 地址（不含端口）
	UnifiedNodes     []int64 `json:"unified_nodes"`       // 统一转发节点列表

	// Driver特定数据 (可选，用于扩展)
	DriverType string                 `json:"driver_type"` // gnb, wireguard
	DriverData map[string]interface{} `json:"driver_data"` // 原始driver数据
}

// VPNPeerHealth 节点健康评估结果
type VPNPeerHealth struct {
	PeerID      int64     `json:"peer_id"`
	IsOnline    bool      `json:"is_online"`
	HealthScore int       `json:"health_score"` // 0-100
	HealthGrade string    `json:"health_grade"` // A/B/C/D
	Anomalies   []string  `json:"anomalies"`    // 异常列表
	LastCheck   time.Time `json:"last_check"`
}

// VPNNetworkSummary 网络摘要
type VPNNetworkSummary struct {
	TotalPeers     int       `json:"total_peers"`
	OnlinePeers    int       `json:"online_peers"`
	DirectPeers    int       `json:"direct_peers"` // 直连节点数
	RelayPeers     int       `json:"relay_peers"`  // 中继节点数
	AvgLatencyMs   float64   `json:"avg_latency_ms"`
	TotalRxBytes   uint64    `json:"total_rx_bytes"`
	TotalTxBytes   uint64    `json:"total_tx_bytes"`
	HealthScore    int       `json:"health_score"`     // 整体健康评分
	IsProcessAlive bool      `json:"is_process_alive"` // VPN进程状态
	LastUpdate     time.Time `json:"last_update"`
}

// IsOnline 判断节点是否在线
func (p *VPNPeer) IsOnline() bool {
	return p.ConnectionStatus == "online"
}

// IsActive 判断节点是否活跃 (参考 GNB 源码逻辑)
// 活跃判断：最近 75 秒内有过 IPv4 或 IPv6 通信
func (p *VPNPeer) IsActive() bool {
	now := time.Now().Unix()

	// 计算距离最后活跃的时间（取 IPv4 和 IPv6 中较小值）
	secondsSinceLastActive := int64(^uint64(0) >> 1) // 初始化为最大 int64

	if p.Addr4UpdateTsSec > 0 {
		diff := now - p.Addr4UpdateTsSec
		if diff < secondsSinceLastActive {
			secondsSinceLastActive = diff
		}
	}

	if p.Addr6UpdateTsSec > 0 {
		diff := now - p.Addr6UpdateTsSec
		if diff < secondsSinceLastActive {
			secondsSinceLastActive = diff
		}
	}

	// 75 秒内有通信认为活跃
	return secondsSinceLastActive <= 75
}

// GetLastActiveTime 获取最后活跃时间（取 IPv4 和 IPv6 中较新的）
func (p *VPNPeer) GetLastActiveTime() int64 {
	if p.Addr4UpdateTsSec > p.Addr6UpdateTsSec {
		return p.Addr4UpdateTsSec
	}
	return p.Addr6UpdateTsSec
}

// GetSecondsSinceLastActive 获取距离最后活跃的秒数
func (p *VPNPeer) GetSecondsSinceLastActive() int64 {
	now := time.Now().Unix()
	lastActive := p.GetLastActiveTime()

	if lastActive == 0 {
		return 9999 // 无时间戳，返回一个大值
	}

	return now - lastActive
}

// IsDirect 判断是否直连
func (p *VPNPeer) IsDirect() bool {
	return p.ConnectionType == "direct_ipv4" ||
		p.ConnectionType == "direct_ipv6" ||
		p.ConnectionType == "dual_stack"
}

// FormatTraffic 格式化流量显示
func (p *VPNPeer) FormatTraffic() string {
	return formatBytes(p.TxBytes) + " ↑ / " + formatBytes(p.RxBytes) + " ↓"
}

func formatBytes(bytes uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
