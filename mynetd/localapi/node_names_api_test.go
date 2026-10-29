package localapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeNamesCachePersistsAndServes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)

	s := NewServer(Config{})
	s.Ops = func(argv []string) (any, error) {
		if strings.Contains(strings.Join(argv, " "), "config cloud") {
			return opsOut(`{"nodes":[{"node_id":"1","node_name":"Mac 笔记本"},{"id":"2","name":"家里路由"}]}`), nil
		}
		return opsOut(`{}`), nil
	}

	// refresh 写入缓存
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/peers/names/refresh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rec.Code, rec.Body.String())
	}
	// 文件落盘
	if _, err := os.Stat(filepath.Join(home, "var", "node_names.json")); err != nil {
		t.Fatalf("names file missing: %v", err)
	}

	// 新 Server（模拟重启）仍能读到
	s2 := NewServer(Config{})
	rec2 := httptest.NewRecorder()
	s2.mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/v1/peers/names", nil))
	body := rec2.Body.String()
	for _, want := range []string{`"1":"Mac 笔记本"`, `"2":"家里路由"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
}

func TestNodeNamesRefreshAuthExpired(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	s := NewServer(Config{})
	s.Ops = func(argv []string) (any, error) {
		return nil, &authTestError{}
	}
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/peers/names/refresh", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"need_login":true`) {
		t.Fatalf("need_login missing: %s", rec.Body.String())
	}
}

type authTestError struct{}

func (*authTestError) Error() string {
	return `HTTP 401: Unauthenticated；自动续期失败: REFRESH_TOKEN_INVALID`
}
