package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// dialNode 用与 mynetd 侧完全相同的握手参数连上网关
// （对照 mynetd/transport/dial_websocket.go:17-23：X-Node-Id + 子协议 mynet.node.v1）。
func dialNode(t *testing.T, url, nodeID string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hdr := http.Header{}
	if nodeID != "" {
		hdr.Set(NodeIDHeader, nodeID)
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader:   hdr,
		Subprotocols: []string{NodeSubprotocol},
	})
	if err != nil {
		t.Fatalf("WS 拨号失败: %v", err)
	}
	return conn
}

// waitForConns 轮询连接数，避免测试里写死 sleep。
func waitForConns(t *testing.T, s *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.Registry().Count() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("连接数未在 5s 内变为 %d，当前 %d", want, s.Registry().Count())
}

// C-3：gateway 不鉴权——无签名、无 Authorization 也能建立 WS 连接并登记。
// 本测试走**真 WS 握手**（httptest.NewRecorder 不支持 Upgrade，用它会测到假行为）。
func TestC3_ConnectHasNoAuth(t *testing.T) {
	s := NewServer("", "internal-secret")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/node/connect"
	conn := dialNode(t, wsURL, "n1")
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	waitForConns(t, s, 1)

	if !s.Registry().IsOnline("n1") {
		t.Fatal("握手成功后连接应已登记")
	}
	if s.Registry().IsVerified("n1") {
		t.Fatal("必须起始为未鉴权（C-4：只有 back 置位后才 verified）")
	}
}

// 缺 X-Node-Id 无法寻址，必须拒；且不能登记出空 nodeID 的连接。
func TestConnectRequiresNodeID(t *testing.T) {
	s := NewServer("", "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/node/connect"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{NodeSubprotocol},
	})
	if err == nil {
		t.Fatal("缺 X-Node-Id 时握手应失败")
	}
	if resp != nil && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("缺 X-Node-Id 应返回 400，实际 %d", resp.StatusCode)
	}
	if s.Registry().Count() != 0 {
		t.Errorf("握手失败不应登记连接，当前 conns=%d", s.Registry().Count())
	}
}

// 连接生命周期：WS 断开后必须摘表。
// 这是「连接状态表真实」的核心判据——旧的占位实现用一个 POST 就登记，
// 且 Unregister 无人调用，节点会永远显示在线。
func TestDisconnectUnregisters(t *testing.T) {
	s := NewServer("", "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/node/connect"
	conn := dialNode(t, wsURL, "n-life")
	waitForConns(t, s, 1)

	if err := conn.Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Fatalf("关闭连接: %v", err)
	}
	waitForConns(t, s, 0)

	if s.Registry().IsOnline("n-life") {
		t.Error("断开后节点仍显示在线 —— Unregister 未生效")
	}
}

// 同 nodeID 重连：新连接登记、旧连接被踢且其 writeLoop 必须退出（否则 goroutine 泄漏）。
func TestReconnectEvictsOldConn(t *testing.T) {
	s := NewServer("", "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/node/connect"

	old := dialNode(t, wsURL, "dup")
	waitForConns(t, s, 1)
	oldConn, _ := s.Registry().Get("dup")

	fresh := dialNode(t, wsURL, "dup")
	defer fresh.Close(websocket.StatusNormalClosure, "bye")

	// 表里始终只应有一条
	waitForConns(t, s, 1)
	cur, _ := s.Registry().Get("dup")
	if cur == oldConn {
		t.Error("重连后表里仍是旧连接 —— Register 未替换")
	}

	// 旧连接的 done 必须被关闭，否则它的 writeLoop 永久阻塞
	select {
	case <-oldConn.Done():
	case <-time.After(5 * time.Second):
		t.Error("被踢的旧连接 done 未关闭 —— writeLoop goroutine 泄漏")
	}

	// 旧 WS 必须被服务端真关掉：否则旧 handler 仍阻塞在 conn.Read，goroutine 泄漏。
	// 判据是「读旧连接会在有限时间内返回错误」，而不是等它自己超时。
	readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := old.Read(readCtx); err == nil {
		t.Error("被踢的旧 WS 仍能读 —— 服务端没有关闭它，旧 handler goroutine 泄漏")
	}
	_ = old.Close(websocket.StatusNormalClosure, "bye")
}

// UnregisterIf 的竞态保护：旧连接收尾时不得误摘新连接。
func TestUnregisterIfDoesNotDropNewerConn(t *testing.T) {
	r := NewRegistry()
	_, old := r.Register("n")
	_, fresh := r.Register("n") // 旧的被踢

	if r.UnregisterIf("n", old) {
		// 旧连接已被新连接替换，摘除应失败
		t.Error("UnregisterIf(旧连接) 应返回 false")
	}
	if !r.IsOnline("n") {
		t.Error("旧连接收尾把新连接误摘了")
	}
	if !r.UnregisterIf("n", fresh) {
		t.Error("UnregisterIf(当前连接) 应返回 true")
	}
	if r.IsOnline("n") {
		t.Error("摘除当前连接后应离线")
	}
}

// Notify 投进 send 的帧必须真的从 WS 发出去（writeLoop 在排空）。
// 没有 writeLoop 时这条会超时——正是「推送等于不存在」的症状。
func TestNotifyReachesNodeOverWS(t *testing.T) {
	s := NewServer("", "tok")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/node/connect"
	conn := dialNode(t, wsURL, "n-push")
	defer conn.Close(websocket.StatusNormalClosure, "bye")
	waitForConns(t, s, 1)

	// C-5：只有已置位的连接才收推送
	s.Registry().MarkVerified("n-push")

	req := httptest.NewRequest("POST", "/v1/notify",
		strings.NewReader(`{"node_id":"n-push","event":"route.refresh","data":{"generation":"7"}}`))
	req.Header.Set("X-Internal-Token", "tok")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("notify 返回 %d: %s", rec.Code, rec.Body.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("节点侧未收到推送帧: %v", err)
	}
	got := string(raw)
	for _, want := range []string{`"event":"route.refresh"`, `"generation":"7"`} {
		if !strings.Contains(got, want) {
			t.Errorf("推送帧缺少 %s，实际: %s", want, got)
		}
	}
}

// 节点侧的 ping 必须得到 pong，否则长连接会被中间设备掐断。
func TestPingGetsPong(t *testing.T) {
	s := NewServer("", "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/node/connect"
	conn := dialNode(t, wsURL, "n-ping")
	defer conn.Close(websocket.StatusNormalClosure, "bye")
	waitForConns(t, s, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"t":"ping","node_id":"n-ping"}`)); err != nil {
		t.Fatalf("写 ping: %v", err)
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("读 pong: %v", err)
	}
	if !strings.Contains(string(raw), `"t":"pong"`) {
		t.Errorf("期望 pong，实际: %s", raw)
	}
}

func TestC4_MarkVerifiedViaHeader(t *testing.T) {
	s := NewServer("", "")
	s.Registry().Register("n1")

	// 模拟 back 响应头置位
	rec := &verifiedRecorder{ResponseWriter: httptest.NewRecorder()}
	rec.Header().Set(VerifiedHeader, "n1")
	rec.WriteHeader(200)
	if rec.nodeID != "n1" {
		t.Fatalf("nodeID=%q", rec.nodeID)
	}
	s.Registry().MarkVerified(rec.nodeID)
	if !s.Registry().IsVerified("n1") {
		t.Fatal("should be verified after header")
	}
}

func TestC5_NotifyOnlyToVerified(t *testing.T) {
	s := NewServer("", "tok")
	s.Registry().Register("n1")

	// 未置位 → 403
	s.Registry().MarkVerified("n1")
	req := httptest.NewRequest("POST", "/v1/notify", strings.NewReader(`{"node_id":"n1","event":"command.queued"}`))
	req.Header.Set("X-Internal-Token", "tok")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("notify verified got %d: %s", rec.Code, rec.Body.String())
	}

	// 未验证节点
	s.Registry().Register("n2")
	req = httptest.NewRequest("POST", "/v1/notify", strings.NewReader(`{"node_id":"n2","event":"command.queued"}`))
	req.Header.Set("X-Internal-Token", "tok")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("notify unverified got %d", rec.Code)
	}
}

// 上游未配置时 relay 必须明确 502。
//
// ⚠️ 本测试原先叫 TestC7_RelayExists，断言写的是 `rec.Code != 502 && rec.Code != 200`
// ——即**把未实现的恒 502 判为 PASS**（AGENTS.md §7.1 D3 的反面教材）。
// 真转发行为见 relay_test.go。
func TestRelayWithoutUpstreamIs502(t *testing.T) {
	s := NewServer("", "")
	req := httptest.NewRequest("GET", "/v1/relay?path=/api/v1/nodes", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 502 {
		t.Fatalf("上游未配置应 502，实际 %d", rec.Code)
	}
}
