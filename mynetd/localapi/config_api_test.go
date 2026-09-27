package localapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubOps 返回 defaultExecMynet 形状，并按 argv 分派本地 config.Store 面。
// 这里**故意**用真实 mynet 逻辑的 JSON 形状，而不是 HTTP 形状 ——
// 验证 localapi 只做壳、不认配置文件格式。
func stubOps(t *testing.T) func(argv []string) (any, error) {
	t.Helper()
	return func(argv []string) (any, error) {
		if len(argv) == 0 {
			return map[string]any{"error": "empty argv"}, nil
		}
		// 按子命令回 JSON stdout
		switch strings.Join(argv, " ") {
		case "config status --json":
			return opsOut(`{"mode":"","initialized":false}`), nil
		case "config use local --json":
			return opsOut(`{"schema":2,"mode":"local","active":{}}`), nil
		case "apply --active":
			return opsOut(`{"applied":true}`), nil
		case "config tenants --json":
			return opsOut(`[]`), nil
		default:
			return opsOut(`{}`), nil
		}
	}
}

func opsOut(stdout string) map[string]any {
	return map[string]any{"argv": nil, "stdout": stdout, "stderr": "", "bin": "stub"}
}

// 配置 API：GET mode / POST mode=local 切换 —— 全走 Ops，不碰配置文件。
func TestConfigModeAPIShell(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	// 摆一份 local intent，供 stub 之外的路径（若有）使用；本测不依赖它
	dir := filepath.Join(root, "conf", "local")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	intent := `{"schema":2,"mode":"local","driver":"gnb","node":{"id":"n1","overlay":"10.0.0.1/32"}}`
	if err := os.WriteFile(filepath.Join(dir, "intent.json"), []byte(intent), 0o600); err != nil {
		t.Fatal(err)
	}

	var got [][]string
	s := NewServer(Config{Listen: "127.0.0.1:0", Token: "t"})
	s.Ops = func(argv []string) (any, error) {
		got = append(got, append([]string(nil), argv...))
		return stubOps(t)(argv)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/mode", nil)
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET mode: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["success"] != true {
		t.Fatalf("GET body: %v", body)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/config/mode", strings.NewReader(`{"mode":"local"}`))
	req.Host = "127.0.0.1:8096"
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST mode: %d %s", rec.Code, rec.Body.String())
	}
	body = map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["success"] != true {
		t.Fatalf("POST body: %v", body)
	}

	// 必须经 Ops，且含 apply
	joined := ""
	for _, a := range got {
		joined += strings.Join(a, " ") + ";"
	}
	if !strings.Contains(joined, "config use local") {
		t.Fatalf("未走执行器切换: %v", got)
	}
	if !strings.Contains(joined, "apply --active") {
		t.Fatalf("切换后未 apply: %v", got)
	}
}

func TestConfigKeysShell(t *testing.T) {
	var got [][]string
	s := NewServer(Config{Listen: "127.0.0.1:0", Token: "t"})
	s.Ops = func(argv []string) (any, error) {
		got = append(got, append([]string(nil), argv...))
		if strings.Contains(strings.Join(argv, " "), "keys") {
			return opsOut(`{"has_private":true,"public_hex":"aabb","public_prefix":"aabb…","algorithm":"ed25519","node_id":"n1"}`), nil
		}
		return opsOut(`{}`), nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/keys", nil)
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET keys: %d %s", rec.Code, rec.Body.String())
	}
	if len(got) == 0 || strings.Join(got[0], " ") != "config keys show --json" {
		t.Fatalf("GET keys argv=%v", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/config/keys", strings.NewReader(`{"generate":true,"vpn_type":"gnb"}`))
	req.Host = "127.0.0.1:8096"
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST keys: %d %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, a := range got {
		if strings.Contains(strings.Join(a, " "), "keys generate") {
			found = true
		}
	}
	if !found {
		t.Fatalf("POST generate 未走执行器: %v", got)
	}
}
