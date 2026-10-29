//go:build embed

package localapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// M5 完整升级：无 fallback 业务台，/ui/ 只跳 SPA。
func TestEmbeddedIndexIsPureRedirectToSPA(t *testing.T) {
	s := NewServer(Config{Listen: "127.0.0.1:0"})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8096/ui/", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("index unavailable: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `location.replace('/gui/')`) {
		t.Fatal("index must redirect to SPA")
	}
	for _, banned := range []string{`data-api=`, `API 调试`, `id="page-config"`} {
		if strings.Contains(body, banned) {
			t.Fatalf("index still has fallback block %s", banned)
		}
	}
}

func TestFallbackCSSHasResponsiveGuard(t *testing.T) {
	s := NewServer(Config{Listen: "127.0.0.1:0"})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8096/ui/", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "viewport") {
		t.Fatal("index missing viewport meta")
	}
}
