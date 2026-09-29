package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// resultSink 假 back：记录节点面转发过来的请求，供断言逐字透传。
type resultSink struct {
	*httptest.Server
	hit      chan struct{}
	method   string
	path     string
	body     string
	nodeID   string
	ts       string
	sig      string
	respCode int
}

func newResultSink(t *testing.T, respCode int) *resultSink {
	t.Helper()
	rs := &resultSink{hit: make(chan struct{}, 4), respCode: respCode}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rs.method = r.Method
		rs.path = r.URL.RequestURI()
		rs.body = string(raw)
		rs.nodeID = r.Header.Get("X-Node-Id")
		rs.ts = r.Header.Get("X-Timestamp")
		rs.sig = r.Header.Get("X-Node-Signature")
		w.WriteHeader(rs.respCode)
		_, _ = w.Write([]byte(`{"success":true}`))
		select {
		case rs.hit <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *resultSink) waitHit(t *testing.T) bool {
	t.Helper()
	select {
	case <-rs.hit:
		return true
	case <-time.After(3 * time.Second):
		return false
	}
}

func (rs *resultSink) waitNoHit(t *testing.T) {
	t.Helper()
	select {
	case <-rs.hit:
		t.Error("上游被打到了 —— 该帧本应在 gateway 侧被拒")
	case <-time.After(300 * time.Millisecond):
	}
}

// sendResultFrame 建一条真 WS 连接并发一个 result 帧。
func sendResultFrame(t *testing.T, gwURL, nodeID string, f nodeResultFrame) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(gwURL, "http") + "/v1/node/connect"
	conn := dialNode(t, wsURL, nodeID)
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "bye") })

	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("写 result 帧: %v", err)
	}
}

func TestAllowedNodeUpstream(t *testing.T) {
	allow := []struct{ method, path string }{
		{http.MethodPatch, "/api/v1/node/commands/1234/result"},
		{http.MethodPatch, "/api/v1/node/commands/cmd-abc_DEF/result"},
		{http.MethodPost, "/api/v1/node/heartbeat"},
	}
	for _, c := range allow {
		if !allowedNodeUpstream(c.method, c.path) {
			t.Errorf("%s %s 应被允许", c.method, c.path)
		}
	}

	deny := []struct{ method, path, why string }{
		{http.MethodPatch, "/api/v1/node/commands/12/result/../../nodes", "路径穿越"},
		{http.MethodPatch, "/api/v1/node/commands//result", "空 id"},
		{http.MethodPatch, "/api/v1/node/commands/12%2Fresult", "含 % 一律拒"},
		{http.MethodPost, "/api/v1/node/commands/12/result", "method 不匹配"},
		{http.MethodPatch, "/api/v1/nodes", "用户面路径不属节点面"},
		{http.MethodGet, "/api/v1/node/heartbeat", "method 不匹配"},
		{http.MethodPatch, "/admin/dashboard", "管理面"},
		{http.MethodPatch, "//evil.com/api/v1/node/heartbeat", "协议相对 URL 逃逸"},
		{http.MethodDelete, "/api/v1/node/commands/12/result", "method 不在白名单"},
	}
	for _, c := range deny {
		if allowedNodeUpstream(c.method, c.path) {
			t.Errorf("%s %s 应被拒（%s）", c.method, c.path, c.why)
		}
	}
}

// 正常路径：签名头与 body 必须**逐字**到达 back——gateway 不做任何密码学，
// 也不能改写 body，否则 back 验签必失败（签名覆盖 sha256(body)）。
func TestResultFrameForwardedVerbatim(t *testing.T) {
	sink := newResultSink(t, 200)
	s := NewServer(sink.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	const (
		body = `{"success":true,"message":"命令执行成功","result":{"stdout":"ok"}}`
		sig  = "8JzMnfrCaUEBikZurTQn4xOlPQiyYlB1HKTcTLaiU3M="
	)
	sendResultFrame(t, ts.URL, "node-1", nodeResultFrame{
		T: "result", NodeID: "node-1",
		Method: http.MethodPatch,
		Path:   "/api/v1/node/commands/cmd-42/result",
		Body:   body, TS: "1700000000", Sig: sig,
	})

	if !sink.waitHit(t) {
		t.Fatal("result 帧未转发到上游")
	}
	if sink.method != http.MethodPatch {
		t.Errorf("上游收到 method = %q, want PATCH", sink.method)
	}
	if sink.path != "/api/v1/node/commands/cmd-42/result" {
		t.Errorf("上游收到 path = %q", sink.path)
	}
	if sink.body != body {
		t.Errorf("body 被改写了 —— 签名会验不过\n got: %s\nwant: %s", sink.body, body)
	}
	if sink.nodeID != "node-1" {
		t.Errorf("X-Node-Id = %q, want node-1", sink.nodeID)
	}
	if sink.ts != "1700000000" {
		t.Errorf("X-Timestamp = %q", sink.ts)
	}
	if sink.sig != sig {
		t.Errorf("X-Node-Signature 未逐字透传\n got: %q\nwant: %q", sink.sig, sig)
	}
}

// 帧里的 node_id 与连接身份不符 → 丢弃，上游不得被打到。
// 否则一个节点能替别的节点报结果。
func TestResultFrameNodeIDMismatchDropped(t *testing.T) {
	sink := newResultSink(t, 200)
	s := NewServer(sink.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	sendResultFrame(t, ts.URL, "node-real", nodeResultFrame{
		T: "result", NodeID: "node-impersonated",
		Method: http.MethodPatch, Path: "/api/v1/node/commands/c1/result",
		Body: `{"success":true}`, TS: "1", Sig: "x",
	})
	sink.waitNoHit(t)
}

// 白名单外的目标 → 丢弃（SSRF 防护）。
func TestResultFrameNonWhitelistedPathDropped(t *testing.T) {
	sink := newResultSink(t, 200)
	s := NewServer(sink.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	sendResultFrame(t, ts.URL, "node-1", nodeResultFrame{
		T: "result", NodeID: "node-1",
		Method: http.MethodPost, Path: "/api/v1/nodes", // 用户面路径，不属节点面
		Body: `{}`, TS: "1", Sig: "x",
	})
	sink.waitNoHit(t)
}

// 缺签名头 → 丢弃。转发过去也只会被 back 拒，不如就地拦掉并留日志。
func TestResultFrameMissingSignatureDropped(t *testing.T) {
	sink := newResultSink(t, 200)
	s := NewServer(sink.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	sendResultFrame(t, ts.URL, "node-1", nodeResultFrame{
		T: "result", NodeID: "node-1",
		Method: http.MethodPatch, Path: "/api/v1/node/commands/c1/result",
		Body: `{"success":true}`, TS: "1700000000", Sig: "", // 缺签名
	})
	sink.waitNoHit(t)
}

// 上游未配置时不得 panic，帧被丢弃即可。
func TestResultFrameWithoutUpstreamIsDropped(t *testing.T) {
	s := NewServer("", "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	sendResultFrame(t, ts.URL, "node-1", nodeResultFrame{
		T: "result", NodeID: "node-1",
		Method: http.MethodPatch, Path: "/api/v1/node/commands/c1/result",
		Body: `{"success":true}`, TS: "1", Sig: "x",
	})
	// 没有上游可打；只要连接还活着、进程没崩就算过
	if s.Registry().Count() != 1 {
		t.Errorf("连接数 = %d, want 1", s.Registry().Count())
	}
}

// 上游返 4xx/5xx（例如验签失败）不得让 WS 连接崩掉——节点应能继续收命令。
func TestResultFrameUpstreamErrorKeepsConnAlive(t *testing.T) {
	sink := newResultSink(t, 401) // back 验签失败
	s := NewServer(sink.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/node/connect"
	conn := dialNode(t, wsURL, "node-1")
	defer conn.Close(websocket.StatusNormalClosure, "bye")
	waitForConns(t, s, 1)

	raw, _ := json.Marshal(nodeResultFrame{
		T: "result", NodeID: "node-1",
		Method: http.MethodPatch, Path: "/api/v1/node/commands/c1/result",
		Body: `{"success":true}`, TS: "1", Sig: "bad",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("写帧: %v", err)
	}
	if !sink.waitHit(t) {
		t.Fatal("帧未转发")
	}

	// 连接必须还活着：再发一个 ping 应能拿到 pong
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"t":"ping"}`)); err != nil {
		t.Fatalf("上游 401 后连接已坏: %v", err)
	}
	_, pong, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("上游 401 后读不到 pong: %v", err)
	}
	if !strings.Contains(string(pong), `"t":"pong"`) {
		t.Errorf("期望 pong，实际 %s", pong)
	}
	if s.Registry().Count() != 1 {
		t.Errorf("连接数 = %d, want 1", s.Registry().Count())
	}
}
