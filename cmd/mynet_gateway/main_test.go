package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		listen string
		want   bool
		why    string
	}{
		{"127.0.0.1:8097", true, "IPv4 回环"},
		{"[::1]:8097", true, "IPv6 回环"},
		{"localhost:8097", true, "localhost 主机名"},
		{":8097", false, "空 host = 绑所有接口，属对外"},
		{"0.0.0.0:8097", false, "显式全接口"},
		{"[::]:8097", false, "IPv6 全接口"},
		{"192.168.0.5:8097", false, "内网地址仍属对外"},
		{"52.77.179.176:443", false, "公网地址"},
		{"not-an-address", false, "解析失败按对外处理（保守）"},
		{"", false, "空串解析失败"},
	}
	for _, c := range cases {
		if got := isLoopback(c.listen); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v（%s）", c.listen, got, c.want, c.why)
		}
	}
}

// 空口令 + 对外监听 = /v1/notify 完全无校验，任何人都能向节点推事件。必须 fail fast。
func TestValidateConfig(t *testing.T) {
	cases := []struct {
		listen        string
		internalToken string
		wantErr       bool
	}{
		{"127.0.0.1:8097", "", false}, // 回环，本地开发免口令
		{"127.0.0.1:8097", "sekret", false},
		{":8097", "sekret", false}, // 对外但有口令
		{":8097", "", true},        // 对外且无口令 → 拒启
		{"0.0.0.0:8097", "", true},
		{"192.168.0.5:8097", "", true},
	}
	for _, c := range cases {
		err := validateConfig(c.listen, c.internalToken)
		if (err != nil) != c.wantErr {
			t.Errorf("validateConfig(listen=%q, internalToken=%q) err=%v, wantErr=%v",
				c.listen, c.internalToken, err, c.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "internal-token") {
			t.Errorf("错误信息应指出如何修（提到 internal-token），实际: %v", err)
		}
	}
}

// freeAddr 取一个空闲端口。run() 自己会 Listen，所以这里探完即放。
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取空闲端口失败: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// waitHealthy 轮询 /healthz 直到可用，避免测试里写死 sleep。
func waitHealthy(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("gateway 未在 5s 内就绪: %s", base)
}

func healthConns(t *testing.T, base string) int {
	t.Helper()
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Data struct {
			Conns int `json:"conns"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("解析 /healthz: %v", err)
	}
	return body.Data.Conns
}

// 端到端：真起进程内的 gateway，用**真 WS 握手**登记节点后 /healthz 必须反映
// 真实连接数，取消 ctx 后 run() 必须干净返回。
func TestRunServesRealConnCountAndShutsDown(t *testing.T) {
	addr := freeAddr(t)
	base := "http://" + addr

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, addr, "http://127.0.0.1:8080", "") }()

	waitHealthy(t, base)

	if got := healthConns(t, base); got != 0 {
		t.Errorf("初始连接数 = %d, want 0", got)
	}

	// 三个节点各建一条真 WS 连接（握手参数与 mynetd 侧一致）
	wsURL := "ws://" + addr + "/v1/node/connect"
	var conns []*websocket.Conn
	for _, id := range []string{"node-aaa", "node-bbb", "node-ccc"} {
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
		hdr := http.Header{}
		hdr.Set("X-Node-Id", id)
		conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
			HTTPHeader:   hdr,
			Subprotocols: []string{"mynet.node.v1"},
		})
		dialCancel()
		if err != nil {
			t.Fatalf("节点 %s WS 拨号失败: %v", id, err)
		}
		conns = append(conns, conn)
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close(websocket.StatusNormalClosure, "bye")
		}
	}()

	if got := waitForConns(t, base, 3); got != 3 {
		t.Errorf("3 条 WS 连接后 /healthz conns = %d, want 3（必须是真实计数，不是写死的 0）", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run() 取消后返回错误: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("cancel() 后 run() 未在 20s 内返回 —— 优雅退出失效")
	}

	// 端口必须真的释放，否则 Shutdown 只是幌子
	if _, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		t.Errorf("run() 返回后 %s 仍可连接 —— 监听未关闭", addr)
	}
}

// waitForConns 轮询 /healthz 直到连接数达标（WS 登记是异步的）。
func waitForConns(t *testing.T, base string, want int) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	got := -1
	for time.Now().Before(deadline) {
		got = healthConns(t, base)
		if got == want {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	return got
}

// 端口被占必须立刻报错，不能静默失败或 panic。
func TestRunFailsFastOnPortInUse(t *testing.T) {
	addr := freeAddr(t)
	blocker, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("占位监听失败: %v", err)
	}
	defer blocker.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = run(ctx, addr, "", "")
	if err == nil {
		t.Fatal("端口被占时 run() 应返回错误，实际返回 nil")
	}
	if !strings.Contains(err.Error(), "监听") {
		t.Errorf("错误应说明是监听失败，实际: %v", err)
	}
}

// 配了口令后 /v1/notify 必须拒绝无口令请求；这是「gateway 不鉴权节点」
// 与「运维入口要保护」的分界。
func TestRunEnforcesInternalTokenOnNotify(t *testing.T) {
	addr := freeAddr(t)
	base := "http://" + addr
	const internalToken = "test-internal-token"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, addr, "", internalToken) }()
	defer func() {
		cancel()
		<-done
	}()

	waitHealthy(t, base)

	body := `{"node_id":"node-x","event":"route.refresh"}`

	// 无口令 → 401
	resp, err := http.Post(base+"/v1/notify", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/notify: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("无口令 /v1/notify = %d, want 401", resp.StatusCode)
	}

	// 错口令 → 401
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/notify", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", "wrong")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/notify: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("错口令 /v1/notify = %d, want 401", resp2.StatusCode)
	}

	// 对口令 → 通过鉴权（节点不在线，故 404 而非 401）
	req3, _ := http.NewRequest(http.MethodPost, base+"/v1/notify", strings.NewReader(body))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("X-Internal-Token", internalToken)
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("POST /v1/notify: %v", err)
	}
	_ = resp3.Body.Close()
	if resp3.StatusCode == http.StatusUnauthorized {
		t.Error("正确口令仍被 401 —— 口令校验逻辑有误")
	}
	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("正确口令 + 节点不在线 = %d, want 404", resp3.StatusCode)
	}
}

// envOr 的回退语义：环境变量优先，空值走默认。
func TestEnvOr(t *testing.T) {
	key := fmt.Sprintf("MYNET_GW_TEST_%d", time.Now().UnixNano())
	if got := envOr(key, "fallback"); got != "fallback" {
		t.Errorf("未设置时 envOr = %q, want fallback", got)
	}
	t.Setenv(key, "from-env")
	if got := envOr(key, "fallback"); got != "from-env" {
		t.Errorf("已设置时 envOr = %q, want from-env", got)
	}
	t.Setenv(key, "")
	if got := envOr(key, "fallback"); got != "fallback" {
		t.Errorf("设为空串时应走默认，envOr = %q", got)
	}
}
