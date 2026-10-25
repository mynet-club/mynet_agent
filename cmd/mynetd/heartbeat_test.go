package main

import (
	"context"
	"encoding/json"
	"fmt"
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

// 后端的六个必填字段一个都不能少（HeartbeatRequest.php:62-67）。
// 少任何一个都是 422，而症状在节点侧只表现为「上报失败」。
func TestHeartbeatHasAllRequiredFields(t *testing.T) {
	send, _ := mustBodies(t, "1245816621804034", metricsFixture)
	p := asMap(t, send)
	for _, k := range []string{
		"node_id", "timestamp", "cpu_usage", "memory_usage", "disk_usage", "connection_count",
	} {
		if _, ok := p[k]; !ok {
			t.Errorf("缺必填字段 %q ——后端会返 422", k)
		}
	}
	if p["node_id"].(float64) != 1245816621804034 {
		t.Errorf("node_id = %v", p["node_id"])
	}
	if p["timestamp"].(float64) <= 0 {
		t.Errorf("timestamp = %v，期望 > 0", p["timestamp"])
	}
}

// ⚠️ 最关键的一条：单位。后端要的是**百分比**（numeric|min:0|max:100），
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
		t.Errorf("memory_usage = %v 超过 100，会被后端的 max:100 拒掉", mem)
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
// 后端验签前会把 node_monitor 置 nil 再用 PHP 重编码（spec/node-keying.md §4.3），
// 所以拿完整体去签，两端 sha256(body) 永远不等 → 清一色 401。
func TestSignBodyExcludesNodeMonitor(t *testing.T) {
	send, sign := mustBodies(t, "1", metricsFixture)

	if !strings.Contains(string(send), `"node_monitor"`) {
		t.Error("发送体应含 node_monitor")
	}
	if strings.Contains(string(sign), `"node_monitor"`) {
		t.Error("签名体不得含 node_monitor —— 否则与后端的重编码结果不一致，全 401")
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
	// last_active_at 必须与顶层 timestamp 同源，否则后端侧看到的
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
		t.Errorf("认不出的驱动不该出现在 vpn_interfaces 里（后端只接受 gnb|wireguard）: %s", send)
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

// node_id 必须是 integer ≥1（后端的 required|integer|min:1）。
// 与其让后端返 422，不如在节点侧就拒 —— 那样错误信息才看得懂。
func TestBadNodeIDRejected(t *testing.T) {
	for _, bad := range []string{"", "abc", "0", "-1", "  "} {
		if _, _, err := heartbeatBodies(bad, metricsFixture); err == nil {
			t.Errorf("node_id=%q 应被拒", bad)
		}
	}
}

// metrics 输出坏掉时要报错，不能拿零值凑一条心跳发出去 ——
// 那会让后端记下一条「cpu 0%、内存 0%」的假数据，比没有数据更糟。
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

// Go 的 json 默认把 < > & 转义成 \u003c，而后端用 PHP 的
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

// 采集侧的异常值不该让整条心跳非法（后端有 max:100）。
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

// 启动采一次，后续纯存活 —— 第二次 beat 不得再 exec mynet（2026-10-15 裁定）。
func TestHeartbeatCollectsMetricsOnlyOnce(t *testing.T) {
	var calls []string
	var sent [][]byte
	h := &heartbeatLoop{
		ops: func(argv []string) (string, string, error) {
			calls = append(calls, strings.Join(argv, " "))
			if len(argv) > 0 && argv[0] == "metrics" {
				return metricsFixture, "", nil
			}
			return "", "", nil
		},
	}
	h.setReporter("1", func(_ context.Context, sendBody, _ []byte) error {
		sent = append(sent, append([]byte(nil), sendBody...))
		return nil
	})

	ctx := context.Background()
	h.beat(ctx) // 首拍：采集 + 上报
	h.beat(ctx) // 第二拍：纯存活
	h.beat(ctx) // 第三拍：纯存活

	var metricsCalls, detectorCalls int
	for _, c := range calls {
		if strings.HasPrefix(c, "metrics") {
			metricsCalls++
		}
		if strings.HasPrefix(c, "detector") {
			detectorCalls++
		}
	}
	if metricsCalls != 1 {
		t.Fatalf("metrics 调用 %d 次，期望 1 次（启动采一次）: %v", metricsCalls, calls)
	}
	if detectorCalls > 1 {
		t.Fatalf("detector 调用 %d 次，期望 ≤1（只在启动补 system.json）: %v", detectorCalls, calls)
	}
	if len(sent) != 3 {
		t.Fatalf("上报 %d 条，期望 3 条（含纯存活拍）", len(sent))
	}

	// 每拍都要有新 timestamp —— 这就是「活着」信号
	var ts []float64
	for _, b := range sent {
		m := asMap(t, b)
		v, _ := m["timestamp"].(float64)
		ts = append(ts, v)
	}
	if ts[0] <= 0 || ts[1] <= 0 || ts[2] <= 0 {
		t.Fatalf("timestamp 未填: %v", ts)
	}
	// 必填字段与 vpn_interfaces 仍来自启动快照（后端契约：缺 vpn 判离线）
	last := asMap(t, sent[2])
	for _, k := range []string{"node_id", "cpu_usage", "memory_usage", "disk_usage", "connection_count"} {
		if _, ok := last[k]; !ok {
			t.Fatalf("存活心跳缺必填字段 %s: %v", k, last)
		}
	}
	if _, ok := last["vpn_interfaces"]; !ok {
		t.Fatalf("存活心跳必须带 vpn_interfaces（后端缺段判离线）: %v", last)
	}
}

// 首采失败则本拍不报、下一拍重试；成功后冻结，不再 exec。
func TestHeartbeatRetriesCollectUntilFirstSuccess(t *testing.T) {
	var metricsCalls, reports int
	fail := true
	h := &heartbeatLoop{
		ops: func(argv []string) (string, string, error) {
			if len(argv) > 0 && argv[0] == "metrics" {
				metricsCalls++
				if fail {
					return "", "boom", fmt.Errorf("exit 1")
				}
				return metricsFixture, "", nil
			}
			return "", "", nil
		},
	}
	h.setReporter("1", func(_ context.Context, _, _ []byte) error {
		reports++
		return nil
	})

	ctx := context.Background()
	h.beat(ctx) // 首采失败
	if reports != 0 {
		t.Fatalf("首采失败仍上报了 %d 条", reports)
	}
	fail = false
	h.beat(ctx) // 重试成功
	h.beat(ctx) // 纯存活
	if metricsCalls != 2 {
		t.Fatalf("metrics 调用 %d 次，期望 2（失败重试 1 + 成功 1）", metricsCalls)
	}
	if reports != 2 {
		t.Fatalf("上报 %d 条，期望 2", reports)
	}
}

// 逐对端状态：只发活跃对端、跳过本机与非法枚举、按 peer_id 去重；
// 并且 node_monitor 整体不进签名体（否则后端验签必 401）。
func TestPeersStatusOnlyActiveDeduped(t *testing.T) {
	metrics := `{
	  "system": {"cpu_percent": 1, "mem_used_mb": 1, "mem_total_mb": 2,
	             "disk_used_percent": 1, "uptime_sec": 1, "interface_up": 1, "load1": 0.1},
	  "vpn": [
	    {"driver": "gnb", "up": true, "interface": "gnb_tun", "peers": 4,
	     "peers_status": [
	       {"peer_id": 1001, "conn_type": "direct_ipv4", "latency": 12.5, "rx": 10, "tx": 20},
	       {"peer_id": 9999, "conn_type": "direct_ipv4", "latency": 1, "rx": 1, "tx": 1},
	       {"peer_id": 1002, "conn_type": "offline", "latency": 1, "rx": 1, "tx": 1}
	     ]},
	    {"driver": "wireguard", "up": true, "interface": "wg0", "peers": 1,
	     "peers_status": [
	       {"peer_id": 1001, "conn_type": "relay", "latency": 5, "rx": 2, "tx": 3}
	     ]}
	  ]
	}`
	send, sign := mustBodies(t, "9999", metrics)

	var got struct {
		NodeMonitor struct {
			PeersStatus []struct {
				PeerID   int64   `json:"peer_id"`
				ConnType string  `json:"conn_type"`
				Latency  float64 `json:"latency"`
				Rx       uint64  `json:"rx"`
				Tx       uint64  `json:"tx"`
				Seen     int64   `json:"seen"`
			} `json:"peers_status"`
		} `json:"node_monitor"`
	}
	if err := json.Unmarshal(send, &got); err != nil {
		t.Fatalf("decode send: %v\n%s", err, send)
	}
	ps := got.NodeMonitor.PeersStatus
	if len(ps) != 1 {
		t.Fatalf("peers_status=%+v，期望 1 条（本机/非法枚举跳过、重复 peer_id 去重）", ps)
	}
	if ps[0].PeerID != 1001 || ps[0].ConnType != "direct_ipv4" || ps[0].Rx != 10 || ps[0].Tx != 20 || ps[0].Seen == 0 {
		t.Fatalf("peer status=%+v", ps[0])
	}
	if strings.Contains(string(sign), "peers_status") || strings.Contains(string(sign), "node_monitor") {
		t.Fatal("签名体不得含 node_monitor/peers_status")
	}
}
