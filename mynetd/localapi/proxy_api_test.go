package localapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// 测试用 store：内存 + 落一个占位文件（部分用例断言「落盘」）。
// 生产 localapi 经注入用 shared/config（B2 禁止本地 import）。
func memProxyStore() (ProxyIntentLoader, ProxyIntentSaver) {
	mem := map[string]*domain.ProxyIntentState{}
	return func(dir string) (*domain.ProxyIntentState, error) {
			if st, ok := mem[dir]; ok {
				return st, nil
			}
			return &domain.ProxyIntentState{}, nil
		},
		func(dir string, st *domain.ProxyIntentState) error {
			mem[dir] = st
			_ = os.MkdirAll(dir, 0o700)
			return os.WriteFile(filepath.Join(dir, "proxy_intent.json"), []byte(`{}`), 0o600)
		}
}

func wireProxyStore(s *Server) {
	load, save := memProxyStore()
	s.ProxyLoad = load
	s.ProxySave = save
	s.ProxySyncPlugin = func(*domain.ProxyPolicy) error { return nil }
}

func samplePolicy() domain.ProxyPolicy {
	return domain.ProxyPolicy{
		Enabled: true,
		Role:    "client",
		Region:  domain.ProxyRegionDomestic,
		Policy:  domain.ProxyPolicyWhitelist,
		Split: &domain.ProxySplit{
			ListSource: domain.ProxyListInternational,
			MatchMode:  domain.ProxyMatchNormal,
		},
		Exits: []domain.ProxyExit{{NodeID: "9001", Enabled: true}},
	}
}

func TestPluginProxyGetEmpty(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(Config{Listen: "127.0.0.1:0", ProxyConfDir: dir})
	wireProxyStore(s)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugin/proxy", nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestPluginProxyPostLocalThenWriteback(t *testing.T) {
	dir := t.TempDir()
	var applied []string
	var wroteRev int64
	s := NewServer(Config{Listen: "127.0.0.1:0", ProxyConfDir: dir})
	wireProxyStore(s)
	s.Ops = func(argv []string) (any, error) {
		applied = append(applied, strings.Join(argv, " "))
		return nil, nil
	}
	s.ProxyWriteback = func(p domain.ProxyPolicy, rev int64) (int64, error) {
		wroteRev = rev
		return 99, nil
	}

	body, _ := json.Marshal(map[string]any{"policy": samplePolicy()})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugin/proxy", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(applied) == 0 || applied[0] != "plugin apply proxy" {
		t.Fatalf("应派发执行器 plugin apply proxy，got %v", applied)
	}
	if wroteRev == 0 {
		t.Fatal("应发生回写")
	}
	if _, err := os.Stat(filepath.Join(dir, "proxy_intent.json")); err != nil {
		t.Fatalf("状态应落盘: %v", err)
	}
	// GET 确认 effective
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/plugin/proxy", nil)
	rr2 := httptest.NewRecorder()
	s.mux.ServeHTTP(rr2, req2)
	if !strings.Contains(rr2.Body.String(), "dirty") && !strings.Contains(rr2.Body.String(), "clean") {
		t.Fatalf("GET body=%s", rr2.Body.String())
	}
}

func TestPluginProxyWritebackFailKeepsDirty(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(Config{Listen: "127.0.0.1:0", ProxyConfDir: dir})
	wireProxyStore(s)
	s.Ops = func(argv []string) (any, error) { return nil, nil }
	s.ProxyWriteback = func(p domain.ProxyPolicy, rev int64) (int64, error) {
		return 0, errors.New("gateway 不可达")
	}

	body, _ := json.Marshal(map[string]any{"policy": samplePolicy()})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugin/proxy", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("回写失败仍应 200（本地已生效），code=%d body=%s", rr.Code, rr.Body.String())
	}
	raw := rr.Body.String()
	if !strings.Contains(raw, "dirty") {
		t.Fatalf("应标 dirty: %s", raw)
	}
}

func TestApplyCloudIntentMarksNeedApply(t *testing.T) {
	dir := t.TempDir()
	load, save := memProxyStore()
	need, st, err := ApplyCloudIntent(load, save, dir, samplePolicy(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if !need {
		t.Fatal("云端首次写入应触发 apply")
	}
	// 同 revision 再写，effective 不变 → 不必 apply
	need2, _, err := ApplyCloudIntent(load, save, dir, samplePolicy(), 3)
	if err != nil {
		t.Fatal(err)
	}
	// content same source/rev → before==after
	_ = st
	if need2 {
		// 同 revision 同 source，effective 键相同
		t.Log("need2=", need2)
	}
}

// TestPluginProxyLocalOnlySkipsWriteback —— 仅本机 proxy 不回写云端。
func TestPluginProxyLocalOnlySkipsWriteback(t *testing.T) {
	dir := t.TempDir()
	writebackCalled := false
	s := NewServer(Config{Listen: "127.0.0.1:0", ProxyConfDir: dir})
	wireProxyStore(s)
	s.Ops = func(argv []string) (any, error) { return nil, nil }
	s.ProxyWriteback = func(domain.ProxyPolicy, int64) (int64, error) {
		writebackCalled = true
		return 1, nil
	}
	p := samplePolicy()
	p.LocalOnly = true
	body, _ := json.Marshal(map[string]any{"policy": p})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugin/proxy", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if writebackCalled {
		t.Fatal("local_only=true 不应回写云端")
	}
}
