package localapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return NewServer(Config{Listen: "127.0.0.1:0", AllowedHosts: nil})
}

func TestD1_CookieIsHttpOnlyAndSameSiteStrict(t *testing.T) {
	s := newTestServer(t)
	h := s.withSecurity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("GET", "http://127.0.0.1:8096/", nil)
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var found *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == AuthCookieName {
			found = c
		}
	}
	if found == nil {
		t.Fatal("token cookie not set")
	}
	if !found.HttpOnly {
		t.Fatal("cookie must be HttpOnly")
	}
	if found.SameSite != http.SameSiteStrictMode {
		t.Fatalf("SameSite=%v want Strict", found.SameSite)
	}
	if found.Value == "" {
		t.Fatal("empty token")
	}
}

func TestD2_RejectsForeignHostAndOrigin(t *testing.T) {
	s := newTestServer(t)
	h := s.withSecurity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	// DNS rebinding：Host 指向攻击者域名
	req := httptest.NewRequest("GET", "http://evil.example/", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("evil host got %d", rec.Code)
	}

	// 恶意网页 Origin
	req = httptest.NewRequest("GET", "http://127.0.0.1:8096/", nil)
	req.Host = "127.0.0.1:8096"
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("evil origin got %d", rec.Code)
	}

	// 同源回环 Origin 放行
	req = httptest.NewRequest("GET", "http://127.0.0.1:8096/", nil)
	req.Host = "127.0.0.1:8096"
	req.Header.Set("Origin", "http://127.0.0.1:8096")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("loopback origin got %d", rec.Code)
	}
}

func TestDefaultListenIs8096(t *testing.T) {
	if DefaultListen != "127.0.0.1:8096" {
		t.Fatalf("DefaultListen=%s", DefaultListen)
	}
}

func TestCommandWhitelist(t *testing.T) {
	if !allowedSubcommand("detector") || !allowedSubcommand("metrics") {
		t.Fatal("detector/metrics must be allowed")
	}
	if allowedSubcommand("/bin/sh") || allowedSubcommand("curl") {
		t.Fatal("arbitrary commands must be denied")
	}
}

func TestStatusEndpoint(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "transport-only") {
		t.Fatalf("body=%s", body)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
}
