package localapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

func TestAccelerationDomainsReturnsCloudAndCustom(t *testing.T) {
	st := &domain.ProxyIntentState{Desktop: &domain.DesktopAccelerationConfig{
		CustomDomains: []string{"example.com"},
	}}
	s := NewServer(Config{ProxyConfDir: t.TempDir()})
	s.ProxyLoad = func(string) (*domain.ProxyIntentState, error) { return st, nil }
	s.Ops = func(argv []string) (any, error) {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "domains status") {
			return map[string]any{"stdout": `{"proxied":["cloud.test","video.example"],"version":"v1"}`}, nil
		}
		return map[string]any{"stdout": `{}`}, nil
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/acceleration/domains", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, needle := range []string{`"cloud_domains"`, `cloud.test`, `example.com`, `"version":"v1"`} {
		if !strings.Contains(body, needle) {
			t.Fatalf("missing %s in %s", needle, body)
		}
	}
}

func TestAccelerationDomainsRefreshUsesExecutorThenReturns(t *testing.T) {
	st := &domain.ProxyIntentState{}
	s := NewServer(Config{ProxyConfDir: t.TempDir()})
	s.ProxyLoad = func(string) (*domain.ProxyIntentState, error) { return st, nil }
	refreshed := false
	s.Ops = func(argv []string) (any, error) {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "domains refresh") {
			refreshed = true
			return map[string]any{"stdout": `{"proxied":["new.test"],"version":"v2","refreshed":true}`}, nil
		}
		if strings.Contains(joined, "domains status") {
			return map[string]any{"stdout": `{"proxied":["new.test"],"version":"v2"}`}, nil
		}
		return map[string]any{"stdout": `{}`}, nil
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/acceleration/domains/refresh", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !refreshed {
		t.Fatal("refresh must go through executor")
	}
	if !strings.Contains(w.Body.String(), `new.test`) {
		t.Fatalf("expected refreshed list: %s", w.Body.String())
	}
}

func TestAccelerationDomainsRefreshRejectsGet(t *testing.T) {
	s := NewServer(Config{ProxyConfDir: t.TempDir()})
	s.ProxyLoad = func(string) (*domain.ProxyIntentState, error) { return &domain.ProxyIntentState{}, nil }
	r := httptest.NewRequest(http.MethodGet, "/api/v1/acceleration/domains/refresh", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
