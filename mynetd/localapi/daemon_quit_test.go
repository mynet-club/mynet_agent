package localapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDaemonQuitRequiresPostAndHook(t *testing.T) {
	s := NewServer(Config{ProxyConfDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/daemon/quit", nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET code=%d want 405", rr.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/daemon/quit", nil)
	rr = httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503，得到 %d", rr.Code)
	}
}

func TestDaemonQuitInvokesHook(t *testing.T) {
	s := NewServer(Config{ProxyConfDir: t.TempDir()})
	called := false
	s.DaemonQuit = func() error { called = true; return nil }
	req := httptest.NewRequest(http.MethodPost, "/api/v1/daemon/quit", nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !called {
		t.Fatalf("code=%d called=%v body=%s", rr.Code, called, rr.Body.String())
	}
}
