//go:build embed

package localapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRootRedirectsToEmbeddedGUI(t *testing.T) {
	s := NewServer(Config{Listen: "127.0.0.1:0"})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8096/", nil)
	req.Host = "127.0.0.1:8096"
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	s.withSecurity(s.mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("root status=%d want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/gui/" {
		t.Fatalf("root Location=%q want /gui/", loc)
	}
}

func TestRootJSONProbeUnchanged(t *testing.T) {
	s := NewServer(Config{Listen: "127.0.0.1:0"})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8096/", nil)
	req.Host = "127.0.0.1:8096"
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	s.withSecurity(s.mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("probe status=%d", rec.Code)
	}
}
