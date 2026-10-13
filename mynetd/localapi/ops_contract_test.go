package localapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// G3.a / CPI-71：注入假 Ops，锁超时/非零退出/坏 JSON/部分状态 的失败合同。
// 不得把失败当成功，也不得回显敏感 argv。

func TestOpsNonZeroExitReturns500WithStderr(t *testing.T) {
	s := NewServer(Config{Token: "t"})
	s.Ops = func(argv []string) (any, error) {
		return map[string]any{
			"argv": argv, "stdout": "", "stderr": "boom", "error": "exit status 1",
		}, errors.New("exit status 1")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/command", strings.NewReader(`{"argv":["status","--json"]}`))
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("want 500 got %d %s", rec.Code, rec.Body.String())
	}
}

func TestOpsTimeoutSurfacesError(t *testing.T) {
	s := NewServer(Config{Token: "t"})
	s.Ops = func(argv []string) (any, error) {
		time.Sleep(10 * time.Millisecond)
		return map[string]any{"error": "context deadline exceeded"}, errors.New("context deadline exceeded")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/command", strings.NewReader(`{"argv":["metrics","--json"]}`))
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("timeout must not be 2xx: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "deadline") && !strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("timeout message missing: %s", rec.Body.String())
	}
}

func TestNodeStatusBadJSONFromExecutor(t *testing.T) {
	s := NewServer(Config{Token: "t"})
	s.Ops = func(argv []string) (any, error) {
		return map[string]any{"stdout": "{not-json", "stderr": "", "argv": argv}, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/node-status", nil)
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("bad JSON must 500: %d %s", rec.Code, rec.Body.String())
	}
}

func TestNodeStatusPartialJSONAllowedWithMissingFields(t *testing.T) {
	// 字段缺失：允许省略可选字段，不得编 0/空串冒充未知（调用方按缺省处理）
	s := NewServer(Config{Token: "t"})
	s.Ops = func(argv []string) (any, error) {
		return map[string]any{
			"stdout": `{"state":"RUNNING","one_line":"ok","process_up":true}`,
			"argv":   argv,
		}, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/node-status", nil)
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("partial JSON should 200: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil || data["state"] != "RUNNING" {
		t.Fatalf("data.state missing: %v", body)
	}
	if _, has := data["uptime_sec"]; has {
		t.Fatal("缺省字段不应被编造成 0")
	}
}

func TestOpsSecretArgvRejectedBeforeOps(t *testing.T) {
	s := NewServer(Config{Token: "t"})
	s.Ops = func(argv []string) (any, error) {
		t.Fatal("must not reach Ops")
		return nil, nil
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/command",
		strings.NewReader(`{"argv":["config","keys","set","--private","aabb"]}`))
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("secret argv should 400: %d", rec.Code)
	}
}
