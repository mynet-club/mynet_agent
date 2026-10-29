package localapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExperienceCapabilitiesDeclareDesktopFeatures(t *testing.T) {
	s := NewServer(Config{})
	s.Ops = func(argv []string) (any, error) {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "form") {
			return map[string]any{"stdout": `{"mode":"pc","choices":["pc"]}`}, nil
		}
		return map[string]any{"stdout": `{}`}, nil
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/experience/capabilities", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, needle := range []string{
		`"form":"pc"`,
		`"desktop_acceleration":true`,
		`"dns_hook":true`,
		`"proxy_policy":false`,
		`"acl":false`,
		`"routes_edit":false`,
		`"plugins":false`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("missing %s in %s", needle, body)
		}
	}
}

func TestExperienceCapabilitiesDeclareGatewayFeatures(t *testing.T) {
	s := NewServer(Config{})
	s.Ops = func(argv []string) (any, error) {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "form") {
			return map[string]any{"stdout": `{"mode":"gw","choices":["pc","gw"]}`}, nil
		}
		return map[string]any{"stdout": `{}`}, nil
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/experience/capabilities", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, needle := range []string{
		`"form":"gw"`,
		`"desktop_acceleration":false`,
		`"proxy_policy":true`,
		`"acl":true`,
		`"routes_edit":true`,
		`"plugins":true`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("missing %s in %s", needle, body)
		}
	}
}

func TestExperienceCapabilitiesDefaultsDesktopWhenExecutorMissingForm(t *testing.T) {
	s := NewServer(Config{})
	s.Ops = func(argv []string) (any, error) {
		return map[string]any{"stdout": `{}`}, nil
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/experience/capabilities", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"form":"pc"`) {
		t.Fatalf("expected desktop default: %s", w.Body.String())
	}
}
