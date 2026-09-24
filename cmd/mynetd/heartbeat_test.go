package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// metricsFixture 是 `mynet metrics --json` 的真实输出形状
// （取自 2026-09-24 本机实测，数值原样保留）。
const metricsFixture = `{
  "collected_at": "2026-09-24T12:32:50Z",
  "hostname": "MacBook-Pro-3.local",
  "system": {
    "cpu_percent": 26.23,
    "mem_used_mb": 11606,
    "mem_total_mb": 16384,
    "load1": 4.59,
    "uptime_sec": 173865,
    "interface_up": 27,
    "disk_used_percent": 6
  },
  "vpn": [
    {"driver":"wireguard","up":true,"interface":"utun6","peers":3,"peers_up":2,"addrs":["10.1.0.2","2001:db8::2"],"rx_bytes":4096,"tx_bytes":2048}
  ],
  "notes": ["..."]
}`

func mustBodies(t *testing.T, nodeID, metrics string) (send, sign []byte) {
	t.Helper()
	s, g, err := heartbeatBodies(nodeID, metrics)
	if err != nil {
		t.Fatalf("heartbeatBodies(%q): %v", nodeID, err)
	}
	return s, g
}

func asMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("解析心跳体失败: %v（原文 %s）", err, b)
	}
	return m
}

// back 的六个必填字段一个都不能少（HeartbeatRequest.php:62-67）。
// 少任何一个都是 422，而症状在节点侧只表现为「上报失败」。
func TestHeartbeatHasAllRequiredFields(t *testing.T) {
	send, _ := mustBodies(t, "1245816621804034", metricsFixture)
	p := asMap(t, send)
	for _, k := range []string{
		"node_id", "timestamp", "cpu_usage", "memory_usage", "disk_usage", "connection_count",
	} {
		if _, ok := p[k]; !ok {
			t.Errorf("缺必填字段 %q —— back 会返 422", k)
		}
	}
	if p["node_id"].(float64) != 1245816621804034 {
		t.Errorf("node_id = %v", p["node_id"])
	}
	if p["timestamp"].(float64) <= 0 {
		t.Errorf("timestamp = %v，期望 > 0", p["timestamp"])
	}
}

// ⚠️ 最关键的一条：单位。back 要的是**百分比**（numeric|min:0|max:100），
// 而 metrics 给的内存是 MB。忘了转换的话 memory_usage 会是 11606，
// 被 max:100 挡下 → 整条心跳 422，而且每条都 422。
func TestHeartbeatConvertsUnitsToPercent(t *testing.T) {
	send, _ := mustBodies(t, "1", metricsFixture)
	p := asMap(t, send)

	mem := p["memory_usage"].(float64)
	// 11606 / 16384 × 100 = 70.838… → 70.84
	if math.Abs(mem-70.84) > 0.01 {
		t.Errorf("memory_usage = %v，期望 ≈70.84（11606/16384×100）；若得到 11606 说明根本没做单位转换", mem)
	}
	if mem > 100 {
		t.Errorf("memory_usage = %v 超过 100，会被 back 的 max:100 拒掉", mem)
	}
	if p["cpu_usage"].(float64) != 26.23 {
		t.Errorf("cpu_usage = %v，期望 26.23（本来就是百分比，不该再换算）", p["cpu_usage"])
	}
	if p["disk_usage"].(float64) != 6 {
		t.Errorf("disk_usage = %v，期望 6", p["disk_usage"])
	}
	if p["connection_count"].(float64) != 3 {
		t.Errorf("connection_count = %v，期望 3（peer 总数）", p["connection_count"])
	}
}

// 签名体必须**不含** node_monitor，发送体必须含。
// back 验签前会把 node_monitor 置 nil 再用 PHP 重编码（spec/node-keying.md §4.3），
// 所以拿完整体去签，两端 sha256(body) 永远不等 → 清一色 401。
func TestSignBodyExcludesNodeMonitor(t *testing.T) {
	send, sign := mustBodies(t, "1", metricsFixture)

	if !strings.Contains(string(send), `"node_monitor"`) {
		t.Error("发送体应含 node_monitor")
	}
	if strings.Contains(string(sign), `"node_monitor"`) {
		t.Error("签名体不得含 node_monitor —— 否则与 back 的重编码结果不一致，全 401")
	}
	// 除了 node_monitor，两份必须一样（否则签的不是发出去的东西）
	var s, g map[string]any
	_ = json.Unmarshal(send, &s)
	_ = json.Unmarshal(sign, &g)
	delete(s, "node_monitor")
	if len(s) != len(g) {
		t.Errorf("去掉 node_monitor 后两份字段数不同: send=%d sign=%d", len(s), len(g))
	}
	for k, v := range g {
		if sv, ok := s[k]; !ok {
			t.Errorf("签名体有 %q 而发送体没有", k)
		} else if !jsonEqual(sv, v) {
			t.Errorf("字段 %q 在两份里不同: %v vs %v", k, sv, v)
		}
	}
}

// node_self 的**七个**字段都是 required_with:node_monitor.node_self ——
// 发一半会 422，而且 422 的报文只提第一个缺失字段（"and 2 more errors"），
// 补一个漏两个很容易。这条测试就是防止那种「看起来只差一个字段」的反复。
func TestNodeSelfAllSevenFields(t *testing.T) {
	send, _ := mustBodies(t, "1", metricsFixture)
	p := asMap(t, send)
	nm, ok := p["node_monitor"].(map[string]any)
	if !ok {
		t.Fatal("缺 node_monitor")
	}
	ns, ok := nm["node_self"].(map[string]any)
	if !ok {
		t.Fatal("缺 node_monitor.node_self")
	}
	for _, k := range []string{
		"is_running", "connected_peers", "total_peers", "uptime_seconds",
		"last_active_at", "rx_bytes", "tx_bytes",
	} {
		if _, ok := ns[k]; !ok {
			t.Errorf("node_self 缺 %q（required_with 会 422）", k)
		}
	}
	if ns["connected_peers"].(float64) != 2 || ns["total_peers"].(float64) != 3 {
		t.Errorf("peers 映射错: connected=%v total=%v", ns["connected_peers"], ns["total_peers"])
	}
	if ns["uptime_seconds"].(float64) != 173865 {
		t.Errorf("uptime_seconds = %v", ns["uptime_seconds"])
	}
	// last_active_at 必须与顶层 timestamp 同源，否则 back 侧看到的
	// 「最后活跃」会比心跳本身还早/还晚，排查时会对不上
	if ns["last_active_at"].(float64) != p["timestamp"].(float64) {
		t.Errorf("last_active_at = %v 与 timestamp = %v 不一致", ns["last_active_at"], p["timestamp"])
	}
	if ns["rx_bytes"].(float64) != 4096 || ns["tx_bytes"].(float64) != 2048 {
		t.Errorf("rx/tx 映射错: %v / %v", ns["rx_bytes"], ns["tx_bytes"])
	}
}

// vpn_interfaces 的 type/status 是枚举（in:gnb,wireguard / in:up,down），
// ifname 是 required_with。送错任何一个都是 422。
func TestVPNInterfaceShape(t *testing.T) {
	send, _ := mustBodies(t, "1", metricsFixture)
	p := asMap(t, send)
	arr, ok := p["vpn_interfaces"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("vpn_interfaces = %v，期望 1 条", p["vpn_interfaces"])
	}
	iface := arr[0].(map[string]any)
	if iface["type"] != "wireguard" {
		t.Errorf("type = %v，期望 wireguard", iface["type"])
	}
	if iface["status"] != "up" {
		t.Errorf("status = %v，期望 up", iface["status"])
	}
	if iface["ifname"] != "utun6" {
		t.Errorf("ifname = %v，期望 utun6", iface["ifname"])
	}
	if iface["peers_count"].(float64) != 3 {
		t.Errorf("peers_count = %v", iface["peers_count"])
	}
}

// 认不出的驱动**不报**，而不是送一个会被 in:gnb,wireguard 拒掉的值。
func TestUnknownDriverIsSkippedNotSent(t *testing.T) {
	m := strings.Replace(metricsFixture, `"driver":"wireguard"`, `"driver":"openvpn"`, 1)
	send, _ := mustBodies(t, "1", m)
	if strings.Contains(string(send), `"vpn_interfaces"`) {
		t.Errorf("认不出的驱动不该出现在 vpn_interfaces 里（back 只接受 gnb|wireguard）: %s", send)
	}
	// 但心跳本身仍要能发（六个必填字段还在）
	p := asMap(t, send)
	if _, ok := p["cpu_usage"]; !ok {
		t.Error("跳过 vpn 段不该影响必填字段")
	}
}

// ifname 为空时整段不发 —— gnb 侧目前拿不到接口名（`gnb_ctl -s` 输出里没有），
// 而 ifname 是 required_with，送空串会 422。
func TestEmptyIfnameSkipsWholeSection(t *testing.T) {
	m := strings.Replace(metricsFixture, `"interface":"utun6"`, `"interface":""`, 1)
	send, _ := mustBodies(t, "1", m)
	if strings.Contains(string(send), `"vpn_interfaces"`) {
		t.Errorf("ifname 为空时不该发 vpn_interfaces（required_with 会 422）: %s", send)
	}
}

// node_id 必须是 integer ≥1（back 的 required|integer|min:1）。
// 与其让 back 返 422，不如在节点侧就拒 —— 那样错误信息才看得懂。
func TestBadNodeIDRejected(t *testing.T) {
	for _, bad := range []string{"", "abc", "0", "-1", "  "} {
		if _, _, err := heartbeatBodies(bad, metricsFixture); err == nil {
			t.Errorf("node_id=%q 应被拒", bad)
		}
	}
}

// metrics 输出坏掉时要报错，不能拿零值凑一条心跳发出去 ——
// 那会让 back 记下一条「cpu 0%、内存 0%」的假数据，比没有数据更糟。
func TestBrokenMetricsRejected(t *testing.T) {
	for _, bad := range []string{"", "not json", `{"system":{}}`} {
		if bad == `{"system":{}}` {
			// 形状对但全是零值：允许（可能是真的采不到），只要 node_id 合法
			if _, _, err := heartbeatBodies("1", bad); err != nil {
				t.Errorf("形状合法的 JSON 不该被拒: %v", err)
			}
			continue
		}
		if _, _, err := heartbeatBodies("1", bad); err == nil {
			t.Errorf("metrics 输出 %q 应被拒", bad)
		}
	}
}

// Go 的 json 默认把 < > & 转义成 \u003c，而 back 用 PHP 的
// json_encode(UNESCAPED_SLASHES|UNESCAPED_UNICODE) 重编码，**不转义**。
// 两端字节不一致 → sha256(body) 不一致 → 401。
func TestEncodeDoesNotEscapeHTML(t *testing.T) {
	type probe struct {
		A string `json:"a"`
	}
	b, err := encodeHeartbeat(probe{A: `x<y>&z/w`})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, `\u003c`) || strings.Contains(s, `\u003e`) || strings.Contains(s, `\u0026`) {
		t.Errorf("HTML 被转义了，与 PHP 的重编码结果不一致 → 签名会对不上: %s", s)
	}
	if strings.Contains(s, `\/`) {
		t.Errorf("斜杠被转义了（PHP 侧用 UNESCAPED_SLASHES）: %s", s)
	}
	// Encoder 会在末尾追加换行，必须去掉，否则签名体多一个字节
	if strings.HasSuffix(s, "\n") {
		t.Error("末尾换行没去掉 —— 签名体会比发送体多一个字节")
	}
}

// 采集侧的异常值不该让整条心跳非法（back 有 max:100）。
func TestClampPercent(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{-5, 0}, {0, 0}, {42.567, 42.57}, {100, 100}, {11606, 100},
		{math.NaN(), 0}, {math.Inf(1), 0}, {math.Inf(-1), 0},
	}
	for _, c := range cases {
		if got := clampPercent(c.in); got != c.want {
			t.Errorf("clampPercent(%v) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

// 内存总量为 0（采集失败）时返回 0，不能是 NaN/Inf ——
// NaN 编码进 JSON 会直接报错，整条心跳发不出去。
func TestMemPercentZeroTotal(t *testing.T) {
	if got := memPercent(1234, 0); got != 0 {
		t.Errorf("memPercent(1234, 0) = %v，期望 0", got)
	}
	if got := memPercent(8192, 16384); got != 50 {
		t.Errorf("memPercent(8192, 16384) = %v，期望 50", got)
	}
}

func TestNormalizeDriver(t *testing.T) {
	cases := map[string]string{
		"gnb": "gnb", "GNB": "gnb", " gnb ": "gnb",
		"wireguard": "wireguard", "wg": "wireguard", "WireGuard": "wireguard",
		"openvpn": "", "": "", "gnb0": "",
	}
	for in, want := range cases {
		if got := normalizeDriver(in); got != want {
			t.Errorf("normalizeDriver(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
