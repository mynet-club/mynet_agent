package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ── `mynet metrics --json` 的输出形状 ───────────────────────────
//
// ⚠️ 刻意**不 import mynet 包**：AGENTS.md §1.2 规定 mynetd 是纯通信层，
// 系统能力（含指标采集与解析）全在执行器。这里通过 exec 拿 JSON、按契约解析，
// 而不是把执行器的类型引进来 —— 引进来等于把系统能力搬回常驻进程，
// 而且 guard 的 B2 会拦（它扫 mynetd 的 import 路径）。
//
// 字段名对齐 `mynet/cmd_metrics.go` 的 json tag；只取心跳要用的那些，
// 执行器以后加字段不会波及这里。
type metricsSnapshot struct {
	Hostname string `json:"hostname"`
	System   struct {
		CPUPercent      float64 `json:"cpu_percent"`
		MemUsedMB       uint64  `json:"mem_used_mb"`
		MemTotalMB      uint64  `json:"mem_total_mb"`
		Load1           float64 `json:"load1"`
		UptimeSec       int64   `json:"uptime_sec"`
		InterfaceUp     int     `json:"interface_up"`
		DiskUsedPercent float64 `json:"disk_used_percent"`
	} `json:"system"`
	VPN []struct {
		Driver    string   `json:"driver"`
		Up        bool     `json:"up"`
		Interface string   `json:"interface"`
		Peers     int      `json:"peers"`
		PeersUp   int      `json:"peers_up"`
		Addrs     []string `json:"addrs"`
		RxBytes   uint64   `json:"rx_bytes"`
		TxBytes   uint64   `json:"tx_bytes"`
	} `json:"vpn"`
}

// ── back 的心跳请求体 ──────────────────────────────────────────
//
// 契约来源：`mynet_api/app/Modules/VPN/Requests/HeartbeatRequest.php:58-103`。
// 六个必填字段 + 可选的 vpn_interfaces / node_monitor。
//
// ⚠️ **单位**：`cpu_usage` / `memory_usage` / `disk_usage` 都是 0-100 的百分比
// （验证规则是 `numeric|min:0|max:100`），而 metrics 给的内存是 **MB**。
// 转换在这里做，别让 back 去猜单位 —— 猜错的症状是「内存使用率 11606%」
// 然后被 max:100 挡下，整条心跳 422。
//
// ⚠️ **字段顺序有意义**：back 验签时会把收到的 JSON 解析成关联数组、
// 去掉 node_monitor、再用 PHP 重编码（数组保持插入顺序）。所以这里的结构体
// 字段顺序就是签名体的顺序，改顺序会让两端签名不一致 → 全 401。
type heartbeatPayload struct {
	NodeID          int64        `json:"node_id"`
	Timestamp       int64        `json:"timestamp"`
	CPUUsage        float64      `json:"cpu_usage"`
	MemoryUsage     float64      `json:"memory_usage"`
	DiskUsage       float64      `json:"disk_usage"`
	ConnectionCount int          `json:"connection_count"`
	VPNInterfaces   []vpnIfaceHB `json:"vpn_interfaces,omitempty"`
	NodeMonitor     *nodeMonitor `json:"node_monitor,omitempty"`
}

// vpnIfaceHB 对齐 HeartbeatRequest 的 vpn_interfaces.*：
// type 只接受 gnb|wireguard，status 只接受 up|down，ifname 是 required_with。
type vpnIfaceHB struct {
	Type       string `json:"type"`
	Ifname     string `json:"ifname"`
	Status     string `json:"status"`
	IP         string `json:"ip,omitempty"`
	PeersCount int    `json:"peers_count,omitempty"`
}

type nodeMonitor struct {
	NodeSelf nodeSelf `json:"node_self"`
}

// nodeSelf 的**七个**字段都是 required_with:node_monitor.node_self
// （HeartbeatRequest.php:99-105）—— 要么整个 node_monitor 不发，要么七个都发。
// 发一半会 422，而 422 的报文只提第一个缺失字段（"and 2 more errors"），
// 很容易补一个漏两个。wanip / addresses / peers_status 是 nullable，可省。
type nodeSelf struct {
	IsRunning      bool   `json:"is_running"`
	ConnectedPeers int    `json:"connected_peers"`
	TotalPeers     int    `json:"total_peers"`
	UptimeSeconds  int64  `json:"uptime_seconds"`
	LastActiveAt   int64  `json:"last_active_at"`
	RxBytes        uint64 `json:"rx_bytes"`
	TxBytes        uint64 `json:"tx_bytes"`
}

// heartbeatBodies 组装心跳，返回**两份** JSON：发送体与签名体。
//
// 为什么不同：back 的 `NodeHeartbeatAuthMiddleware` 在验签前会把 `node_monitor`
// 置 nil 再重编码（`spec/node-keying.md` §4.3 —— 它体量大且不参与签名）。
// 所以签的必须是「不含 node_monitor」的那份；拿完整体去签，两端算出的
// 签名永远不等，症状是清一色 401 而不是「某个字段错了」。
//
// metricsJSON 是 `mynet metrics --json` 的 stdout 原文。
func heartbeatBodies(nodeID, metricsJSON string) (sendBody, signBody []byte, err error) {
	id, convErr := strconv.ParseInt(strings.TrimSpace(nodeID), 10, 64)
	if convErr != nil || id <= 0 {
		return nil, nil, fmt.Errorf("node_id 非法（back 要求 integer 且 ≥1）: %q", nodeID)
	}

	var snap metricsSnapshot
	if err := json.Unmarshal([]byte(metricsJSON), &snap); err != nil {
		return nil, nil, fmt.Errorf("解析 metrics 输出失败: %w", err)
	}

	p := heartbeatPayload{
		NodeID:      id,
		Timestamp:   time.Now().Unix(),
		CPUUsage:    clampPercent(snap.System.CPUPercent),
		MemoryUsage: memPercent(snap.System.MemUsedMB, snap.System.MemTotalMB),
		DiskUsage:   clampPercent(snap.System.DiskUsedPercent),
		// connection_count 用 VPN peer 总数：节点没有别的「连接」概念，
		// 而 back 要的是 integer ≥0。
		ConnectionCount: totalPeers(snap),
	}

	for _, v := range snap.VPN {
		if !v.Up {
			continue
		}
		typ := normalizeDriver(v.Driver)
		if typ == "" {
			// back 的验证是 in:gnb,wireguard —— 送别的值整条心跳 422。
			// 认不出来的驱动就不报，别把一条能过的心跳弄成过不了。
			continue
		}
		if v.Interface == "" {
			// ifname 是 back 的 required_with:vpn_interfaces，空值会让整条心跳 422，
			// 所以拿不到接口名时只能不发这一段。
			//
			// ⚠️ 但别以为「不发」是安全的降级：back 的 isVpnInterfaceDown()
			// 第一行就是 `if (empty($vpnInterface)) return true;`
			// （NodeHeartbeatController.php，注释写着「如果没有 VPN 接口信息，
			// 视为网络不可用」）→ 走 markNodeOffline 并**提前 return**，
			// 连 node_monitor 都不处理。也就是「少报一个接口」会被上报成
			// 「节点离线 + 监控数据全丢」。
			// 此处曾写着「isVpnInterfaceDown(null) 不会因此判节点下线」——
			// 那是没核实的错误假设，Phase 6 实测正好相反。
			// 真正的修法在 probeGNB：接口名从 bundle 的 gnb.options.ifname 取，
			// 不依赖 gnb_ctl（它的状态输出里没有接口名，且读 ctl block 要 root）。
			continue
		}
		p.VPNInterfaces = append(p.VPNInterfaces, vpnIfaceHB{
			Type:       typ,
			Ifname:     v.Interface,
			Status:     "up",
			IP:         firstAddr(v.Addrs),
			PeersCount: v.Peers,
		})
	}

	p.NodeMonitor = &nodeMonitor{NodeSelf: nodeSelf{
		IsRunning:      len(snap.VPN) > 0,
		ConnectedPeers: onlinePeers(snap),
		TotalPeers:     totalPeers(snap),
		UptimeSeconds:  snap.System.UptimeSec,
		// last_active_at 用当前时间：这条心跳本身就是「刚刚还活着」的证据。
		// metrics 里没有「最后一次 peer 活动」这个量，编一个反而更假。
		LastActiveAt: p.Timestamp,
		RxBytes:      totalRx(snap),
		TxBytes:      totalTx(snap),
	}}

	sendBody, err = encodeHeartbeat(p)
	if err != nil {
		return nil, nil, err
	}
	// 签名体：node_monitor 置 nil，靠 omitempty 让它整个消失
	signP := p
	signP.NodeMonitor = nil
	signBody, err = encodeHeartbeat(signP)
	if err != nil {
		return nil, nil, err
	}
	return sendBody, signBody, nil
}

// encodeHeartbeat 用 SetEscapeHTML(false) 编码。
//
// ⚠️ 这不是风格问题：Go 的 json.Marshal 默认把 `<` `>` `&` 转义成 `\u003c` 等，
// 而 back 侧用 PHP 的 `json_encode(JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE)`
// 重编码，**不转义**。两端字节不一致 → sha256(body) 不一致 → 签名不匹配 → 401。
// 同样的坑在命令回报那条链上已经踩过（见 spec/node-keying.md §4.5 第 2 点）。
//
// 另外 Encoder 会在末尾追加换行，必须去掉 —— 否则签名体多一个字节。
func encodeHeartbeat(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("编码心跳: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// normalizeDriver 把执行器报的 driver 名收敛到 back 接受的枚举。
// 认不出来返回空串（调用方据此跳过，而不是送一个会 422 的值）。
func normalizeDriver(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "gnb":
		return "gnb"
	case "wireguard", "wg":
		return "wireguard"
	default:
		return ""
	}
}

func firstAddr(addrs []string) string {
	for _, a := range addrs {
		if a != "" {
			return a
		}
	}
	return ""
}

func totalPeers(snap metricsSnapshot) int {
	n := 0
	for _, v := range snap.VPN {
		n += v.Peers
	}
	return n
}

func onlinePeers(snap metricsSnapshot) int {
	n := 0
	for _, v := range snap.VPN {
		n += v.PeersUp
	}
	return n
}

func totalRx(snap metricsSnapshot) uint64 {
	var n uint64
	for _, v := range snap.VPN {
		n += v.RxBytes
	}
	return n
}

func totalTx(snap metricsSnapshot) uint64 {
	var n uint64
	for _, v := range snap.VPN {
		n += v.TxBytes
	}
	return n
}

// clampPercent 收敛到 back 接受的 0-100，并保留两位小数。
// 不 clamp 的话，采集侧一个异常值就会让整条心跳被 max:100 挡下。
func clampPercent(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return math.Round(v*100) / 100
}

// memPercent 把 MB 换成百分比。总量为 0 时返回 0 而不是 NaN/Inf ——
// 采集失败（比如拿不到页大小）不该让心跳变成非法值。
func memPercent(usedMB, totalMB uint64) float64 {
	if totalMB == 0 {
		return 0
	}
	return clampPercent(float64(usedMB) * 100 / float64(totalMB))
}
