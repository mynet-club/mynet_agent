package main

import (
	"testing"
)

// G3 缺口⑥：tun 未起也报一格 —— vpn_interfaces 里带 status=down 的行
// （后端契约既有值 HeartbeatRequest：vpn_interfaces.*.status in:up,down，
// 此前从不发）。「服务 active 但 tun 未起/零流量」在心跳里要可见：
// 期望口名在场、状态明说 down，而不是整段消失（后端对 down 行与缺段
// 同判离线，行为不变；差别是信号可见）。
func TestHeartbeatReportsDownCellWhenTunMissing(t *testing.T) {
	const metricsDown = `{
  "collected_at": "2026-10-14T12:00:00Z",
  "system": {
    "cpu_percent": 1.0, "mem_used_mb": 100, "mem_total_mb": 1000,
    "load1": 0.1, "uptime_sec": 10, "interface_up": 3, "disk_used_percent": 5
  },
  "vpn": [
    {"driver":"gnb","up":false,"interface":"gnb_tun"}
  ]
}`
	send, sign := mustBodies(t, "1", metricsDown)
	p := asMap(t, send)

	ifaces, _ := p["vpn_interfaces"].([]any)
	if len(ifaces) != 1 {
		t.Fatalf("tun 未起也该报一格（status=down）: %v", p["vpn_interfaces"])
	}
	m, _ := ifaces[0].(map[string]any)
	if m["status"] != "down" {
		t.Fatalf("down 格的 status 必须是 down: %v", m)
	}
	if m["ifname"] != "gnb_tun" || m["type"] != "gnb" {
		t.Fatalf("down 格要带期望口名与驱动: %v", m)
	}
	if _, hasIP := m["ip"]; hasIP {
		t.Fatalf("tun 没起不该编 IP: %v", m)
	}

	// 签名体与发送体的 vpn_interfaces 一致（node_monitor 之外不裁字段）。
	// 顺序敏感性由既有签名测试覆盖，这里只锁「down 格进签名体」。
	s := asMap(t, sign)
	if _, ok := s["vpn_interfaces"]; !ok {
		t.Fatalf("down 格也必须进签名体（两端同体才验得过）: %v", s)
	}
}

// up 格不受影响：up 行照旧 status=up（回归锁）。
func TestHeartbeatUpCellUnchanged(t *testing.T) {
	send, _ := mustBodies(t, "1", metricsFixture)
	p := asMap(t, send)
	ifaces, _ := p["vpn_interfaces"].([]any)
	if len(ifaces) != 1 {
		t.Fatalf("fixture 应有一格 up: %v", p["vpn_interfaces"])
	}
	m, _ := ifaces[0].(map[string]any)
	if m["status"] != "up" || m["ifname"] != "utun6" {
		t.Fatalf("up 格不应变化: %v", m)
	}
}
