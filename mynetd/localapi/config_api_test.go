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

	// 仅私钥 → keys set 走 **stdin**（--private-stdin，C1-S1：私钥不进 argv），
	// 且不带 --public（推导归执行器，B2 薄壳不算密钥）
	got = nil
	secret := strings.Repeat("ab", 64)
	var stdinArgv []string
	var stdinGot string
	s.ExecStdin = func(argv []string, stdin string) (map[string]any, error) {
		stdinArgv = append([]string(nil), argv...)
		stdinGot = stdin
		return opsOut(`{"ok":true}`), nil
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/config/keys",
		strings.NewReader(`{"private_hex":"`+secret+`"}`))
	req.Host = "127.0.0.1:8096"
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST keys private-only: %d %s", rec.Code, rec.Body.String())
	}
	if len(got) != 0 {
		t.Fatalf("keys set 应走 ExecStdin 而非 Ops: %v", got)
	}
	setArgv := strings.Join(stdinArgv, " ")
	if !strings.Contains(setArgv, "--private-stdin") {
		t.Fatalf("keys set 应带 --private-stdin: %q", setArgv)
	}
	if strings.Contains(setArgv, secret) {
		t.Fatalf("私钥不得进 argv: %q", setArgv)
	}
	if strings.Contains(setArgv, "--public") {
		t.Fatalf("private-only 不应带 --public: %q", setArgv)
	}
	if strings.TrimSpace(stdinGot) != secret {
		t.Fatalf("私钥应经 stdin 传递: %q", stdinGot)
	}
	s.ExecStdin = nil

	// 既无私钥也不 generate → 400，不打执行器
	got = nil
	req = httptest.NewRequest(http.MethodPost, "/api/v1/config/keys",
		strings.NewReader(`{"public_hex":"cd"}`))
	req.Host = "127.0.0.1:8096"
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("无私钥应 400，得 %d %s", rec.Code, rec.Body.String())
	}
	if len(got) != 0 {
		t.Fatalf("400 路径不得调执行器: %v", got)
	}
}

// 流程闸门第②步「选节点」：setup_only 只落 profile + mode.json，
// 绝不停旧驱动 / apply / service start —— 此刻还没密钥，全量切换会停在 apply。
func TestConfigActiveSetupOnly(t *testing.T) {
	var got [][]string
	s := NewServer(Config{Listen: "127.0.0.1:0", Token: "t"})
	s.Ops = func(argv []string) (any, error) {
		got = append(got, append([]string(nil), argv...))
		return opsOut(`{}`), nil
	}

	// 缺 node → 400 且不打执行器
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/active",
		strings.NewReader(`{"tenant":"t1","setup_only":true}`))
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("缺 node 应 400: %d %s", rec.Code, rec.Body.String())
	}
	if len(got) != 0 {
		t.Fatalf("缺 node 不该调执行器: %v", got)
	}

	// 成功路径：恰好一条 setup --online，没有任何切换动作
	req = httptest.NewRequest(http.MethodPost, "/api/v1/config/active",
		strings.NewReader(`{"tenant":"t1","node":"n9","setup_only":true}`))
	req.Host = "127.0.0.1:8096"
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("setup_only 应 200: %d %s", rec.Code, rec.Body.String())
	}
	if len(got) != 1 || strings.Join(got[0], " ") != "setup --online --tenant t1 --node-id n9" {
		t.Fatalf("setup_only argv=%v，必须只有 setup --online 一条", got)
	}
	for _, a := range got {
		j := strings.Join(a, " ")
		if strings.Contains(j, "service") || strings.Contains(j, "apply") || strings.Contains(j, "config use") {
			t.Fatalf("setup_only 混入全量切换动作: %s", j)
		}
	}
}

func TestConfigPrepareStopsAtBusinessError(t *testing.T) {
	var calls []string
	s := NewServer(Config{Listen: "127.0.0.1:0", Token: "t"})
	s.Ops = func(argv []string) (any, error) {
		calls = append(calls, strings.Join(argv, " "))
		if len(calls) == 2 {
			return opsOut(`{"error":"need_login"}`), nil
		}
		return opsOut(`{"has_private":true}`), nil
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/prepare", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 502 || len(calls) != 2 || strings.Contains(strings.Join(calls, " "), "pull") {
		t.Fatalf("unexpected pipeline: %d %v", rec.Code, calls)
	}
}
func TestConfigActiveWireGuard(t *testing.T) {
	for _, vpn := range []string{"wireguard", "invalid"} {
		var called string
		s := NewServer(Config{Listen: "127.0.0.1:0", Token: "t"})
		s.Ops = func(argv []string) (any, error) { called = strings.Join(argv, " "); return opsOut(`{}`), nil }
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/active", strings.NewReader(`{"tenant":"t1","node":"42","setup_only":true,"vpn_type":"`+vpn+`"}`))
		req.Host = "127.0.0.1:8096"
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		if vpn == "wireguard" {
			if rec.Code != 200 || !strings.Contains(called, "--vpn-type wireguard") {
				t.Fatalf("lost vpn: %s", called)
			}
		} else if rec.Code != 400 || called != "" {
			t.Fatal("invalid VPN invoked executor")
		}
	}
}

func TestConfigPrepareSequence(t *testing.T) {
	var calls []string
	s := NewServer(Config{Listen: "127.0.0.1:0", Token: "t"})
	s.Ops = func(argv []string) (any, error) {
		calls = append(calls, strings.Join(argv, " "))
		return opsOut(`{"ok":true}`), nil
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/prepare", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	want := "config keys ensure --json|config keys sync --connect --json|config pull --download-only --json"
	if rec.Code != 200 || strings.Join(calls, "|") != want {
		t.Fatalf("unexpected prepare %d %v", rec.Code, calls)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/config/prepare", nil)
	req.Host = "127.0.0.1:8096"
	rec = httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 405 || len(calls) != 3 {
		t.Fatal("read request caused mutations")
	}
}
