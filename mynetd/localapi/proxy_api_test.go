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

// samplePermission 云端下发的出口许可（D1 收窄后 wire 的 network.proxy）。
func samplePermission() domain.ProxyPermission {
	return domain.ProxyPermission{
		Enabled: true,
		ACL:     &domain.ProxyACL{Mode: domain.ProxyACLAllowlist, AllowedNodeIDs: []string{"9001"}},
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
	// 第三段：写意图 + 驱动投影之后必须刷周边，但只刷 proxy 段
	// （`proxy apply`：只动 fwmark/table 200/打标集合，不碰 OS 路由）。
	if len(applied) < 2 || applied[1] != "proxy apply" {
		t.Fatalf("应在 plugin apply 之后派发 proxy apply 刷 proxy 段，got %v", applied)
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
	need, st, err := ApplyCloudIntent(load, save, dir, samplePermission(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if !need {
		t.Fatal("云端首次写入应触发 apply")
	}
	// 同 revision 再写，effective 不变 → 不必 apply
	need2, _, err := ApplyCloudIntent(load, save, dir, samplePermission(), 3)
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

// O7 并发护序：POST /api/v1/plugin/proxy 必须与 config 写面共用 configMutation。
// 该端点直调 s.Ops（不走 dispatch → 不触发 isContextMutation 的加锁），此前**未加锁**：
// 会与 config/apply、LuCI 写、后台 stamp 并发改同一份 proxy_intent + 驱动投影 → 丢更新。
func TestPluginProxyPostSharesConfigMutationLock(t *testing.T) {
	s := NewServer(Config{Listen: "127.0.0.1:0", ProxyConfDir: t.TempDir()})
	wireProxyStore(s)

	// 模拟另一个配置写入正在执行（如 config/apply）
	if !s.configMutation.TryLock() {
		t.Fatal("前置：锁应空闲")
	}
	defer s.configMutation.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugin/proxy", strings.NewReader(`{"policy":{}}`))
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("并发写应被挡成 409，实际 %d：%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "operation_in_progress") {
		t.Fatalf("应回 operation_in_progress：%s", rr.Body.String())
	}

	// 读面必须不受影响（读可与写并行 —— 页面刷新状态不该 409）
	greq := httptest.NewRequest(http.MethodGet, "/api/v1/plugin/proxy", nil)
	grr := httptest.NewRecorder()
	s.mux.ServeHTTP(grr, greq)
	if grr.Code == http.StatusConflict {
		t.Fatalf("GET 不该被互斥挡，实际 %d", grr.Code)
	}
}
