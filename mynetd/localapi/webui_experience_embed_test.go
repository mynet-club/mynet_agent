//go:build embed

package localapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedExperienceStylesheetServed(t *testing.T) {
	s := NewServer(Config{Listen: "127.0.0.1:0"})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8096/ui/", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("embedded index unavailable: %d", rec.Code)
	}
	href := regexp.MustCompile(`<link rel="stylesheet" href="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	if len(href) != 2 {
		t.Fatal("stylesheet link missing")
	}
	base, _ := url.Parse("http://127.0.0.1:8096/ui/")
	target, _ := url.Parse(href[1])
	req = httptest.NewRequest(http.MethodGet, base.ResolveReference(target).String(), nil)
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/css") || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("embedded stylesheet inaccessible: %d %v", rec.Code, rec.Header())
	}
	if rec.Body.Len() == 0 {
		t.Fatal("empty stylesheet")
	}
}
