package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent-V2/mynet/keys"
)

func TestNewBackClientDerivesRelayURL(t *testing.T) {
	id := &nodeIdentity{nodeID: "n1"}
	cases := []struct {
		gatewayWS string
		want      string
	}{
		{"ws://127.0.0.1:8710/v1/node/connect", "http://127.0.0.1:8710/v1/relay"},
		{"wss://gw.mynet.club/v1/node/connect", "https://gw.mynet.club/v1/relay"},
		{"http://10.0.0.5:8710/v1/node/connect", "http://10.0.0.5:8710/v1/relay"},
		{"https://gw.example.com", "https://gw.example.com/v1/relay"},
	}
	for _, c := range cases {
		bc, err := newBackClient(c.gatewayWS, id)
		if err != nil {
			t.Errorf("newBackClient(%q): %v", c.gatewayWS, err)
			continue
		}
		if bc.relayBase != c.want {
			t.Errorf("newBackClient(%q).relayBase = %q, want %q", c.gatewayWS, bc.relayBase, c.want)
		}
	}
}

func TestNewBackClientRejectsBadGatewayURL(t *testing.T) {
	id := &nodeIdentity{nodeID: "n1"}
	for _, bad := range []string{"", "   ", ":///no-scheme", "ftp://host:21/x", "/just/a/path"} {
		if _, err := newBackClient(bad, id); err == nil {
			t.Errorf("newBackClient(%q) 应报错", bad)
		}
	}
}

// 请求必须：打到 relay、path 作为 query 传递、三个签名头齐全、
// 且签名的 path 与 back 将看到的一致（带前导斜杠）。
func TestBackClientDoSignsAndUsesRelay(t *testing.T) {
	var (
		gotPath   string
		gotQuery  string
		gotNode   string
		gotTS     string
		gotSig    string
		gotMethod string
		gotBody   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("path")
		gotNode = r.Header.Get("X-Node-Id")
		gotTS = r.Header.Get("X-Timestamp")
		gotSig = r.Header.Get("X-Node-Signature")
		gotMethod = r.Method
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	ident, err := loadNodeIdentity(writeConf(t, validConf()))
	if err != nil {
		t.Fatal(err)
	}
	// 直接把 relayBase 指向测试服务器，省掉 ws→http 推导（那部分另有测试）
	bc := &backClient{relayBase: srv.URL + "/v1/relay", id: ident, hc: srv.Client()}

	body := []byte(`{"foo":"bar"}`)
	code, resp, err := bc.do(context.Background(), http.MethodPost, "/api/v1/node/heartbeat", body)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if code != 200 {
		t.Errorf("code = %d, want 200", code)
	}
	if string(resp) != `{"success":true}` {
		t.Errorf("resp = %s", resp)
	}

	if gotPath != "/v1/relay" {
		t.Errorf("应打到 /v1/relay，实际 %q", gotPath)
	}
	if gotQuery != "/api/v1/node/heartbeat" {
		t.Errorf("path query = %q, want /api/v1/node/heartbeat", gotQuery)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotBody != string(body) {
		t.Errorf("body 未原样发出: %q", gotBody)
	}
	if gotNode != vecNodeID {
		t.Errorf("X-Node-Id = %q, want %q", gotNode, vecNodeID)
	}
	if gotTS == "" || gotSig == "" {
		t.Fatal("签名头缺失")
	}

	// 签名必须能被 C-2 向量密钥验过，且 path 用的是**带前导斜杠**的完整路径
	// （back 的 signedPath() 返回 '/'.$request->path()，差一个斜杠就全 401）
	verifier, err := keys.NewNodeFaceSigner(vecNodePriv, vecBackPub)
	if err != nil {
		t.Fatal(err)
	}
	if !verifier.Verify(http.MethodPost, "/api/v1/node/heartbeat", gotTS, gotBody, gotSig) {
		t.Error("签名验不过 —— back 会 401")
	}
	// 反面：用不带前导斜杠的 path 验签必须失败，证明斜杠真的进了签名
	if verifier.Verify(http.MethodPost, "api/v1/monitor/heartbeat", gotTS, gotBody, gotSig) {
		t.Error("不带前导斜杠也能验过 —— 说明 path 口径没有被签名锁定")
	}
}

// back 的 rt_cmd_id 是 unsignedBigInteger，JSON 里是数字；
// 解析必须容忍数字形态，否则命令拉取会整体失败。
func TestPullPendingCommandsParsesNumericIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"node_id": 1245816621804034,
			"node_status": 1,
			"commands": [
				{"id": 9007199254740993, "command": "mynet.route.refresh",
				 "action": "config.refresh", "params": {"node_id": "77"}, "created_at": 1790000000}
			]
		}`))
	}))
	defer srv.Close()

	ident, err := loadNodeIdentity(writeConf(t, validConf()))
	if err != nil {
		t.Fatal(err)
	}
	bc := &backClient{relayBase: srv.URL + "/v1/relay", id: ident, hc: srv.Client()}

	resp, err := bc.pullPendingCommands(context.Background())
	if err != nil {
		t.Fatalf("pullPendingCommands: %v", err)
	}
	if !resp.Success {
		t.Error("success 应为 true")
	}
	if len(resp.Commands) != 1 {
		t.Fatalf("应解析出 1 条命令，实际 %d", len(resp.Commands))
	}
	c := resp.Commands[0]
	if c.ID.String() != "9007199254740993" {
		t.Errorf("id = %q, want 9007199254740993（超出 JS 安全整数，必须原样保留）", c.ID.String())
	}
	if c.Command != "mynet.route.refresh" {
		t.Errorf("command = %q", c.Command)
	}
	if c.Params["node_id"] != "77" {
		t.Errorf("params 未解析: %v", c.Params)
	}
}

// back 返回非 2xx 时必须报错并带上状态码，不能当成「没有命令」。
func TestPullPendingCommandsSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"message":"signature mismatch"}`))
	}))
	defer srv.Close()

	ident, _ := loadNodeIdentity(writeConf(t, validConf()))
	bc := &backClient{relayBase: srv.URL + "/v1/relay", id: ident, hc: srv.Client()}

	_, err := bc.pullPendingCommands(context.Background())
	if err == nil {
		t.Fatal("401 应报错")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误应带状态码，实际: %v", err)
	}
}

// back 报 success=false 时也必须报错。
func TestPullPendingCommandsSurfacesBusinessFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":"missing X-Node-Id"}`))
	}))
	defer srv.Close()

	ident, _ := loadNodeIdentity(writeConf(t, validConf()))
	bc := &backClient{relayBase: srv.URL + "/v1/relay", id: ident, hc: srv.Client()}

	_, err := bc.pullPendingCommands(context.Background())
	if err == nil {
		t.Fatal("success=false 应报错")
	}
	if !strings.Contains(err.Error(), "missing X-Node-Id") {
		t.Errorf("错误应保留 back 的原文，实际: %v", err)
	}
}

// 响应不是合法 JSON 时必须报错并带上原文片段，否则排障无从下手。
func TestPullPendingCommandsRejectsMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>502 Bad Gateway</html>`))
	}))
	defer srv.Close()

	ident, _ := loadNodeIdentity(writeConf(t, validConf()))
	bc := &backClient{relayBase: srv.URL + "/v1/relay", id: ident, hc: srv.Client()}

	_, err := bc.pullPendingCommands(context.Background())
	if err == nil {
		t.Fatal("非法 JSON 应报错")
	}
	if !strings.Contains(err.Error(), "502 Bad Gateway") {
		t.Errorf("错误应带上响应原文片段，实际: %v", err)
	}
}

// 心跳的**签名体与发送体不同**：back 的 NodeHeartbeatAuthMiddleware 验签前会把
// node_monitor 置 nil 再用 PHP 重编码（spec/node-keying.md §4.3），所以节点侧
// 必须签「不含 node_monitor」的那份、发完整的那份。
//
// 这条测试把这件事钉死：用签名体验签**必须通过**，用发送体验签**必须失败**。
// 如果哪天有人「顺手统一」成一份，back 侧会清一色 401，而节点侧看不出任何异常
// —— 症状是「鉴权失败」而不是「你签错了体」，极难定位。
func TestReportHeartbeatSignsBodyWithoutNodeMonitor(t *testing.T) {
	var gotPath, gotMethod, gotTS, gotSig, gotBody, gotNode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Query().Get("path")
		gotMethod = r.Method
		gotTS = r.Header.Get("X-Timestamp")
		gotSig = r.Header.Get("X-Node-Signature")
		gotNode = r.Header.Get("X-Node-Id")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	ident, err := loadNodeIdentity(writeConf(t, validConf()))
	if err != nil {
		t.Fatal(err)
	}
	bc := &backClient{relayBase: srv.URL + "/v1/relay", id: ident, hc: srv.Client()}

	send, sign, err := heartbeatBodies(ident.nodeID, metricsFixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := bc.reportHeartbeat(context.Background(), send, sign); err != nil {
		t.Fatalf("reportHeartbeat: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s，期望 POST", gotMethod)
	}
	if gotPath != "/api/v1/node/heartbeat" {
		t.Errorf("path = %q（差一个斜杠就会全 401）", gotPath)
	}
	if gotTS == "" || gotSig == "" {
		t.Errorf("签名头缺失: ts=%q sig=%q", gotTS, gotSig)
	}
	if gotNode != ident.nodeID {
		t.Errorf("X-Node-Id = %q，期望 %q", gotNode, ident.nodeID)
	}

	// 发出去的必须是**完整体**（back 要拿 node_monitor 落库）
	if gotBody != string(send) {
		t.Error("发送体与组装结果不一致")
	}
	if !strings.Contains(gotBody, `"node_monitor"`) {
		t.Error("发送体应含 node_monitor")
	}

	// 签名覆盖的必须是**不含 node_monitor** 的那份
	if !ident.signer.Verify(gotMethod, gotPath, gotTS, string(sign), gotSig) {
		t.Error("用签名体验签失败 —— 签名口径不对")
	}
	if ident.signer.Verify(gotMethod, gotPath, gotTS, gotBody, gotSig) {
		t.Error("用发送体也能验签通过 —— 说明签的是完整体，back 侧会 401")
	}
}

// 上报失败必须**报错**，不能静默吞掉 —— 否则「节点在线但 back 没数据」
// 这种状态会一直查不出来。
func TestReportHeartbeatSurfacesFailure(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
	}{
		{"验签失败", 401, `{"success":false,"error":"签名不匹配"}`},
		{"验证失败", 422, `{"message":"cpu_usage 必须介于 0 和 100 之间"}`},
		{"网关上游挂了", 502, `bad gateway`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.code)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()

			ident, err := loadNodeIdentity(writeConf(t, validConf()))
			if err != nil {
				t.Fatal(err)
			}
			bc := &backClient{relayBase: srv.URL + "/v1/relay", id: ident, hc: srv.Client()}
			send, sign, err := heartbeatBodies(ident.nodeID, metricsFixture)
			if err != nil {
				t.Fatal(err)
			}
			err = bc.reportHeartbeat(context.Background(), send, sign)
			if err == nil {
				t.Fatalf("HTTP %d 应报错，却返回 nil", c.code)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(c.code)) {
				t.Errorf("错误里应带状态码 %d，实际: %v", c.code, err)
			}
		})
	}
}
