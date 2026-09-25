package localapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// wizardReq 打一个本机 API 端点，返回 recorder。
func wizardReq(t *testing.T, s *Server, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "127.0.0.1:8096"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	return rec
}

// TestWizardLoginNeverPutsPasswordInArgv —— 本文件最重要的一条。
//
// 口令若进 argv，任何本机进程 `ps` 都看得见；而向导是**装机第一步**，
// 那台机器上可能还有别的服务在跑。所以它必须走 stdin
// （mynet auth login --password-stdin）。这条性质不能靠肉眼看代码确认。
func TestWizardLoginNeverPutsPasswordInArgv(t *testing.T) {
	s := newTestServer(t)
	var gotArgv []string
	var gotStdin string
	s.ExecStdin = func(argv []string, stdin string) (map[string]any, error) {
		gotArgv, gotStdin = argv, stdin
		return map[string]any{"stdout": "已登录", "argv": argv}, nil
	}

	const secret = "S3cr3t-Pa55w0rd-Do-NOT-Leak" // GUARD-OK 测试哨兵：用来验证它不会泄漏进 argv/响应，不是真凭据
	rec := wizardReq(t, s, http.MethodPost, "/api/v1/wizard/login", `{
		"api_base_url":"https://api.mynet.club",
		"email":"a@b.c",
		"password":"`+secret+`"
	}`)
	if rec.Code != 200 {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}

	// ① argv 里绝不能出现口令（会进 ps）
	if strings.Contains(strings.Join(gotArgv, " "), secret) {
		t.Fatalf("口令出现在 argv 里 → ps 可见：%v", gotArgv)
	}
	// ② 口令必须经 stdin 交给 --password-stdin
	if !strings.Contains(strings.Join(gotArgv, " "), "--password-stdin") {
		t.Fatalf("argv 里缺 --password-stdin：%v", gotArgv)
	}
	if !strings.Contains(gotStdin, secret) {
		t.Fatalf("stdin 里没有口令，登录不可能成功：%q", gotStdin)
	}
	// ③ 响应里绝不能回显口令
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("响应里回显了口令：%s", rec.Body.String())
	}
}

// TestWizardLoginSanitizesOutput —— 钉住「过滤**真的被应用**」。
//
// 上面那条只证明了 argv 不含口令；它**证不了** sanitizeWizardOut 被调用了 ——
// 因为 mock 返回的 stdout 本来就不含秘密，滤不滤都一样。
// 这条让 mock 故意吐出带 token 的 stdout，只有过滤真的生效，响应才是干净的。
func TestWizardLoginSanitizesOutput(t *testing.T) {
	s := newTestServer(t)
	s.ExecStdin = func(argv []string, stdin string) (map[string]any, error) {
		return map[string]any{
			"stdout": "登录成功 auth_token=" + strings.Repeat("A", 40), // GUARD-OK 假 token 夹具：验证 sanitizeWizardOut 真的会遮它
			"stderr": "debug 42|" + strings.Repeat("B", 40),
			"argv":   argv,
		}, nil
	}

	rec := wizardReq(t, s, http.MethodPost, "/api/v1/wizard/login", `{
		"api_base_url":"https://api.mynet.club","email":"a@b.c","password":"pw"
	}`)
	body := rec.Body.String()
	if strings.Contains(body, strings.Repeat("A", 40)) {
		t.Fatalf("stdout 里的 token 没被过滤就发给了浏览器：%s", body)
	}
	if strings.Contains(body, strings.Repeat("B", 40)) {
		t.Fatalf("stderr 里的 PAT 没被过滤就发给了浏览器：%s", body)
	}
}

// TestWizardLoginRequiresAllThreeFields 空字段要拒，别让 mynet 自己去报模糊的错。
func TestWizardLoginRequiresAllThreeFields(t *testing.T) {
	s := newTestServer(t)
	s.ExecStdin = func(argv []string, stdin string) (map[string]any, error) {
		t.Fatal("参数不全时不该被执行")
		return nil, nil
	}
	rec := wizardReq(t, s, http.MethodPost, "/api/v1/wizard/login", `{"email":"a@b.c"}`)
	if rec.Code != 400 {
		t.Fatalf("缺字段应 400，实际 %d", rec.Code)
	}
}

// TestWizardWhitelistIncludesWizardSubcommands —— 向导三步依赖这三个子命令
// 能经 /api/v1/command 走（它们不含秘密；口令另走专用端点）。
func TestWizardWhitelistIncludesWizardSubcommands(t *testing.T) {
	for _, c := range []string{"auth", "tenants", "node"} {
		if !allowedSubcommand(c) {
			t.Errorf("白名单缺 %s —— 向导无法经 /api/v1/command 驱动它", c)
		}
	}
	// 反向：任意路径仍然要拒
	for _, c := range []string{"../etc/passwd", "/bin/sh", "bash"} {
		if allowedSubcommand(c) {
			t.Errorf("白名单不该放行 %q", c)
		}
	}
}

// TestWizardStateReflectsDiskNotMemory —— 重开浏览器/重启 mynetd 状态要一致。
// （CPI-68 的教训：落盘结果比命令退出码可信。）
func TestWizardStateReflectsDiskNotMemory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	s := newTestServer(t)

	// 全空 → 向导在第一步
	rec := wizardReq(t, s, http.MethodGet, "/api/v1/wizard/state", "")
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data["logged_in"] != false || body.Data["node_configured"] != false {
		t.Fatalf("空 home 应报未登录/未配置：%v", body.Data)
	}

	// 写出凭证与节点身份 → 状态要跟着变
	mustMkdirWrite(t, filepath.Join(home, "conf", "credential.json"),
		`{"user_email":"a@b.c","token":"x","refresh_token":"y","api_base_url":"https://api.mynet.club"}`)
	mustMkdirWrite(t, filepath.Join(home, "conf", "mynet.conf"),
		"NODE_ID=123\nNODE_PRIV_HEX=ab\nNODE_PUB_HEX=cd\nBACK_PUB_HEX=ef\n")
	mustMkdirWrite(t, filepath.Join(home, "var", "bundle.json"), `{"generation":"g1"}`)

	rec = wizardReq(t, s, http.MethodGet, "/api/v1/wizard/state", "")
	body.Data = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data["logged_in"] != true || body.Data["node_configured"] != true || body.Data["bundle_downloaded"] != true {
		t.Fatalf("产物齐了状态没跟着变：%v", body.Data)
	}
	if body.Data["node_id"] != "123" {
		t.Fatalf("node_id 应从 mynet.conf 读出，got %v", body.Data["node_id"])
	}
	if body.Data["user_email"] != "a@b.c" {
		t.Fatalf("user_email 应从 credential.json 读出，got %v", body.Data["user_email"])
	}

	// ⚠️ 私钥绝不能出现在状态里 —— 它只该躺在 mynet.conf
	if strings.Contains(rec.Body.String(), "ab") && strings.Contains(rec.Body.String(), "PRIVATE") {
		t.Fatal("状态里出现了私钥")
	}
}

// TestRedactSecretsSparesPublicKeyMasksTokens —— 脱敏的边界。
//
// ⚠️ 第一版规则「32+ hex 一律遮」把 back 的**节点面公钥**也遮了，而那是
// CLI 特意打印出来给人核对的（它不是秘密，性质同 JWKS）。过度脱敏让信息
// 不可用，还制造「这里有个秘密」的错觉。所以规则按**真实威胁**收紧：
// PAT 形状无条件遮；长 hex 只在该行提到 secret 关键字时遮。
func TestRedactSecretsSparesPublicKeyMasksTokens(t *testing.T) {
	// ① Sanctum PAT：`数字|长串` —— 无条件遮
	pat := "token 42|" + strings.Repeat("A", 40) + " end"
	if out := redactLine(pat); strings.Contains(out, strings.Repeat("A", 40)) {
		t.Fatalf("PAT 没被遮：%s", out)
	}
	// ② 提到 secret 关键字的行：长 hex 遮掉
	secretLine := "auth_token=" + strings.Repeat("ab", 20) // GUARD-OK 假 token 夹具：验证带 secret 关键字的行会遮长 hex
	if out := redactLine(secretLine); strings.Contains(out, strings.Repeat("ab", 20)) {
		t.Fatalf("token 行的长 hex 没被遮：%s", out)
	}
	// ③ **公钥要可见** —— 这是脱敏边界的关键一侧
	pubLine := "back 节点面公钥: " + strings.Repeat("ab", 32)
	if out := redactLine(pubLine); out != pubLine {
		t.Fatalf("公钥不该被遮（它是给人核对的）：%s", out)
	}
	// ④ 普通文字要保留
	plain := "已登录 z2o-a@mynet.club user_id=6258330675511267"
	if out := redactLine(plain); out != plain {
		t.Fatalf("普通文字不该被改：%s", out)
	}
}

// webui 的 id 必须唯一。此前「启动隧道」在步骤 3、4 各有一份 id="wiz-start"，
// getElementById 只绑到第一份（步骤 3 那个）—— 用户落到步骤 4（已有配置、
// 重开页面）时，看得见的按钮是**死的**，点了没反应，观感就是「向导没把
// gnb 跑起来」。用 class/data-* 绑定后，这条钉住不再回潮。
func TestWebUIHasNoDuplicateIDs(t *testing.T) {
	raw, err := os.ReadFile("webui/index.html")
	if err != nil {
		t.Skip("webui 不在本包目录（构建布局变了）")
	}
	counts := map[string]int{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(string(raw), -1) {
		counts[m[1]]++
	}
	for id, n := range counts {
		if n > 1 {
			t.Errorf("id=%q 出现 %d 次 —— 重复 id 让 getElementById 只绑到第一份，其余按钮是死的", id, n)
		}
	}
	// 启动/退出必须用 data-wiz 绑定（3、4 两步各一份），且 JS 里统一 querySelectorAll
	for _, want := range []string{`data-wiz="start"`, `data-wiz="restart"`, `data-wiz="logout"`, `querySelectorAll('[data-wiz]')`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("webui 缺 %s", want)
		}
	}
}

func mustMkdirWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// relay 两跳：GUI→mynetd→gateway。路径白名单在**本机这一跳也要查**，
// 不能假设上游一定挡得住。
func TestRelayRejectsPathOutsideWhitelist(t *testing.T) {
	s := NewServer(Config{Token: "t"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://127.0.0.1:8096/api/v1/relay?path=/api/v1/admin/secret", nil)
	req.Host = "127.0.0.1:8096"
	s.withSecurity(http.HandlerFunc(s.handleRelay)).ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("code=%d want 403（白名单外必须拒）body=%s", rec.Code, rec.Body.String())
	}
}

func TestRelayRequiresPath(t *testing.T) {
	s := NewServer(Config{Token: "t"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://127.0.0.1:8096/api/v1/relay", nil)
	req.Host = "127.0.0.1:8096"
	s.withSecurity(http.HandlerFunc(s.handleRelay)).ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("code=%d want 400", rec.Code)
	}
}

// 未配 MYNET_GATEWAYS 时要明确 502，而不是悄悄空转。
func TestRelayWithoutGatewayIs502(t *testing.T) {
	os.Unsetenv("MYNET_GATEWAYS")
	s := NewServer(Config{Token: "t"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://127.0.0.1:8096/api/v1/relay?path=/api/v1/nodes", nil)
	req.Host = "127.0.0.1:8096"
	s.withSecurity(http.HandlerFunc(s.handleRelay)).ServeHTTP(rec, req)
	if rec.Code != 502 {
		t.Fatalf("code=%d want 502 body=%s", rec.Code, rec.Body.String())
	}
}

// /gui/ 的静态资源：URL 带 /gui 前缀，但磁盘上没有 gui/ 这一层。
// 不剥前缀就会去找 <root>/gui/assets/… —— index.html 200、JS/CSS 全 404。
func TestGUIAssetsStripPrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>idx</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Setenv("MYNET_GUI_DIR", dir)
	defer os.Unsetenv("MYNET_GUI_DIR")

	s := NewServer(Config{Token: "t"})
	h := s.withSecurity(http.HandlerFunc(s.handleGUI))

	// index
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://127.0.0.1:8096/gui/", nil)
	req.Host = "127.0.0.1:8096"
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "idx") {
		t.Fatalf("index code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 资源：路径含 /gui/，磁盘上是 assets/app.js
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "http://127.0.0.1:8096/gui/assets/app.js", nil)
	req.Host = "127.0.0.1:8096"
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("asset code=%d（应剥掉 /gui 前缀命中 assets/app.js）", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "console.log") {
		t.Fatalf("asset body=%s", rec.Body.String())
	}
}
