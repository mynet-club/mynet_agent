package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestC3_ConnectHasNoAuth(t *testing.T) {
	s := NewServer("", "internal-secret")
	h := s.Handler()

	// 无签名、无 Authorization 也能连上（gateway 不鉴权）
	req := httptest.NewRequest("GET", "/v1/node/connect", nil)
	req.Header.Set("X-Node-Id", "n1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("connect got %d: %s", rec.Code, rec.Body.String())
	}
	if !s.Registry().IsOnline("n1") {
		t.Fatal("conn should be registered")
	}
	if s.Registry().IsVerified("n1") {
		t.Fatal("must start unverified (C-4)")
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

func TestC7_RelayExists(t *testing.T) {
	s := NewServer("", "")
	req := httptest.NewRequest("GET", "/v1/relay", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	// 上游未配置 → 502，但路径存在
	if rec.Code != 502 && rec.Code != 200 {
		t.Fatalf("relay got %d", rec.Code)
	}
}
