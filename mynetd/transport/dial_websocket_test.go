package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// pong 是传输层控制帧，不得被当成业务帧喂给 HandleFrame——
// 否则每 30s 一次 ping 就刷一条「无法识别的帧」错误日志，把真问题淹掉。
func TestIsPong(t *testing.T) {
	yes := []string{
		`{"t":"pong"}`,
		`{"t":"pong","node_id":"1245816621804034"}`,
		`{"node_id":"1","t":"pong"}`,
		`  {"t":"pong"}  `,
	}
	for _, f := range yes {
		if !isPong([]byte(f)) {
			t.Errorf("isPong(%s) = false, want true", f)
		}
	}

	no := []string{
		`{"t":"ping"}`,                        // 自己发的，不该被当 pong
		`{"event":"route_changed","data":{}}`, // 业务事件帧
		`{"id":"c1","op":"metrics"}`,          // 裸命令帧
		`{}`,                                  // 空对象
		`not json`,                            // 非法 JSON
		``,                                    // 空帧
		`{"t":"pongx"}`,                       // 前缀相同但不是 pong
	}
	for _, f := range no {
		if isPong([]byte(f)) {
			t.Errorf("isPong(%s) = true, want false", f)
		}
	}
}

// 半死连接必须被主动判死。
//
// 场景是现网最难查的那一类：中间设备（NAT/负载均衡）静默丢包时 TCP 不发 FIN，
// conn.Read 一直阻塞、重连循环永不触发，于是节点「日志里全是 connected，
// 但一条下发都收不到」。判据：pong 超时 → WebSocketDialer 返回错误 → Run 重连
// → 重连触发 OnConnect 补拉。
func TestSilentConnectionIsDroppedByIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		// 只收不回：模拟「连接还在、pong 不再来」
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c, err := New(Config{
		Gateways:     []string{"ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/node/connect"},
		NodeID:       "n1",
		PingInterval: 10 * time.Millisecond,
		IdleTimeout:  150 * time.Millisecond,
	}, Handlers{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- WebSocketDialer(context.Background(), c) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("静默连接应返回错误，实际返回 nil")
		}
		if !strings.Contains(err.Error(), "pong") {
			t.Errorf("错误应说明是 pong 超时判死，实际: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("半死连接未被判活超时断开 —— 节点会永久收不到下发")
	}
}

// 有事件下行（哪怕没 pong）也算活着：繁忙期误判断开会把连接打摆子。
func TestDownstreamFramesKeepConnectionAlive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		// 每 20ms 推一个事件帧，永不回 pong
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
				if err := conn.Write(ctx, websocket.MessageText,
					[]byte(`{"event":"route_changed"}`)); err != nil {
					return
				}
			}
		}()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	var events int32
	c, err := New(Config{
		Gateways:     []string{"ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/node/connect"},
		NodeID:       "n1",
		PingInterval: 10 * time.Millisecond,
		IdleTimeout:  300 * time.Millisecond,
	}, Handlers{OnEvent: func(string, map[string]string) { atomic.AddInt32(&events, 1) }}, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = WebSocketDialer(ctx, c)

	// 期望：连接一直活着，最后因 ctx 超时结束（ctx deadline exceeded），
	// 而不是被 idle 判死。
	if err != nil && strings.Contains(err.Error(), "pong") {
		t.Errorf("有下行帧时不应 pong 超时判死，实际: %v", err)
	}
	if atomic.LoadInt32(&events) == 0 {
		t.Error("下行事件帧未被分派")
	}
}
