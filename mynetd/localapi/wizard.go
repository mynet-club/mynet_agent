package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 「首次运行向导：登录 → 选节点 → 下载配置」的**本机 API 面**。
//
// 设计定位（用户 2026-09-25 裁定）：
//
//	· **交互在本机 web 页面**（这里）与 OpenWrt 的 LuCI 页面上
//	· `mynet` 命令行提供**非交互原语**，本面只是它的薄封装
//	· **绝不做进 TUI**（TUI 在旧仓 mynet_agent/internal/tui，与本项目无关）
//
// 所以本文件刻意只做三件事，不越界：
//  1. 把浏览器的请求转成 `mynet …` 子命令
//  2. 口令**只经 stdin** 交给 `mynet auth login --password-stdin`
//     —— 走 argv 会进 `ps`，任何本机进程都看得见
//  3. 回报「向导走到哪一步」（状态由磁盘上的产物决定，不靠内存计数器）
//
// ⚠️ 浏览器**从不接触任何 token**：凭证在 `$MYNET_HOME/conf/credential.json`
// （0600），由 mynet 持有；本面只转述「已登录 / 未登录」。
// 这条是刻意的：token 进了浏览器就有 XSS 泄漏面，而本机页面虽然同源，
// 但内嵌 webui 与第三方内容同处一域时会放大风险。
func (s *Server) registerWizardRoutes() {
	s.mux.HandleFunc("/api/v1/wizard/login", s.handleWizardLogin)
	s.mux.HandleFunc("/api/v1/wizard/logout", s.handleWizardLogout)
	s.mux.HandleFunc("/api/v1/wizard/state", s.handleWizardState)
}

// handleWizardLogin 口令走 stdin，绝不进 argv。
//
//	POST {"api_base_url":"https://…","email":"you@example.com","password":"…"}
func (s *Server) handleWizardLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "POST only"})
		return
	}
	var body struct {
		APIBaseURL string `json:"api_base_url"`
		Email      string `json:"email"`
		Password   string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"success": false, "message": "bad json"})
		return
	}
	if body.APIBaseURL == "" || body.Email == "" || body.Password == "" {
		writeJSON(w, 400, map[string]any{"success": false, "message": "api_base_url / email / password 均必填"})
		return
	}

	argv := []string{"auth", "login", "--api", body.APIBaseURL, "--email", body.Email, "--password-stdin"}
	out, err := s.execWithStdin(argv, body.Password+"\n")

	// ⚠️ 响应里绝不回显口令或 token
	safe := sanitizeWizardOut(out)
	if err != nil {
		writeJSON(w, 502, map[string]any{"success": false, "message": err.Error(), "data": safe})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": safe})
}

func (s *Server) handleWizardLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "POST only"})
		return
	}
	// auth logout 会先吊销服务端会话再删本地凭证（见 mynet/cmd_auth.go）
	out, err := s.execMynetStd([]string{"auth", "logout"})
	if err != nil {
		writeJSON(w, 502, map[string]any{"success": false, "message": err.Error(), "data": sanitizeWizardOut(out)})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": sanitizeWizardOut(out)})
}

// handleWizardState —— 向导进度。**由磁盘上的产物决定**，不靠内存计数器：
// 重开浏览器、重启 mynetd，状态都要一致。这正是「落盘结果比命令退出码可信」
// （CPI-68 的教训）在 UI 侧的体现。
func (s *Server) handleWizardState(w http.ResponseWriter, r *http.Request) {
	home := os.Getenv("MYNET_HOME")
	if home == "" {
		home = "."
	}
	conf := filepath.Join(home, "conf")

	st := map[string]any{
		"home":              home,
		"logged_in":         fileNonEmpty(filepath.Join(conf, "credential.json")),
		"node_configured":   fileNonEmpty(filepath.Join(conf, "mynet.conf")),
		"bundle_downloaded": fileNonEmpty(filepath.Join(home, "var", "bundle.json")),
		"system_detected":   fileNonEmpty(filepath.Join(home, "var", "system.json")),
	}

	// 从 mynet.conf 读 NODE_ID（这是**节点身份**，不是秘密；私钥不读不发）
	if kv := readKV(filepath.Join(conf, "mynet.conf")); kv != nil {
		st["node_id"] = kv["NODE_ID"]
		st["back_pub"] = kv["BACK_PUB_HEX"]
	}
	// 从 credential.json 只取**非敏感**字段
	if b, err := os.ReadFile(filepath.Join(conf, "credential.json")); err == nil {
		var c map[string]any
		if json.Unmarshal(b, &c) == nil {
			st["user_email"] = c["user_email"]
			st["api_base_url"] = c["api_base_url"]
			st["auth_token_expires_at"] = c["auth_token_expires_at"]
			st["refresh_token_expires_at"] = c["refresh_token_expires_at"]
		}
	}

	writeJSON(w, 200, map[string]any{"success": true, "data": st})
}

// execWithStdin 跑 mynet <argv> 并把 stdinText 喂给它。
// 与 defaultExecMynet 的区别只有一个：能喂 stdin —— 登录用。
//
// 可经 s.ExecStdin 注入（与 s.Ops 同款），为的是让测试能断言
// **口令没进 argv** —— 那条安全性质不能靠肉眼看代码确认。
func (s *Server) execWithStdin(argv []string, stdinText string) (map[string]any, error) {
	if s.ExecStdin != nil {
		return s.ExecStdin(argv, stdinText)
	}
	bin := os.Getenv("MYNET_BIN")
	if bin == "" {
		bin = "mynet"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Stdin = strings.NewReader(stdinText)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	out := map[string]any{"argv": argv, "stdout": stdout.String(), "stderr": stderr.String(), "bin": bin}
	if err != nil {
		out["error"] = err.Error()
		return out, err
	}

	return out, nil
}

func (s *Server) execMynetStd(argv []string) (map[string]any, error) {
	if s.Ops == nil {
		s.Ops = defaultExecMynet
	}
	v, err := s.Ops(argv)
	if err != nil {
		return map[string]any{"argv": argv, "error": err.Error()}, err
	}
	m, _ := v.(map[string]any)
	if m == nil {
		m = map[string]any{"argv": argv}
	}

	return m, nil
}

// sanitizeWizardOut 去掉 stdout/stderr 里可能出现的口令与 token。
//
// mynet 自己已尽量不打印敏感值（credential 落盘路径、公钥指纹等），
// 但 **这里再兜一道**：本面的输出直接进浏览器，而浏览器侧会展示给用户看
// —— 值得多一道过滤，而不是赌上游永远不打印。
func sanitizeWizardOut(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k == "argv" {
			// argv 里不带口令（走 stdin），但 email 会带 —— 保留，用户要看
			out[k] = v
			continue
		}
		if s, ok := v.(string); ok {
			out[k] = redactSecrets(s)
		} else {
			out[k] = v
		}
	}

	return out
}

// redactSecrets 把**真的像秘密**的片段遮掉，同时放过公钥。
//
// ⚠️ 第一版规则是「32+ 的 hex 一律遮」，实测把 back 的**节点面公钥**也遮成了
// `*******[hex]` —— 而那正是 CLI 特意打印出来给人核对的值（它不是秘密，
// 性质同 JWKS）。过度脱敏让信息变得不可用，还制造「这里有个秘密」的错觉。
//
// 收紧后的规则（对着 mynet 真实输出来定，不是凭想象）：
//
//	· Sanctum PAT 的形状 `数字|长串` —— 一律遮。mynet 正常不打印它，
//	  真出现就是哪条路径把 token 漏了出来，那必须遮。
//	· 长 hex —— **只在该行含 secret 关键字时**遮
//	  （token / refresh / priv / private / password / secret）。
//	  公钥、校验和、node_id 这些不带关键字的长 hex 保持可见。
//
// 兜底仍然是「输出走 textContent 进 <pre>」，不经 innerHTML —— 这一层防 XSS，
// 与脱敏是两条独立的防线。
func redactSecrets(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = redactLine(ln)
	}

	return strings.Join(lines, "\n")
}

var secretKeywords = []string{"token", "refresh", "priv", "private", "password", "secret"}

func lineHasSecretKeyword(ln string) bool {
	low := strings.ToLower(ln)
	for _, k := range secretKeywords {
		if strings.Contains(low, k) {
			return true
		}
	}

	return false
}

func redactLine(ln string) string {
	// ① Sanctum PAT：`数字|长串`（tokenId|plainTextToken）—— 无条件遮
	out := redactPAT(ln)
	// ② 该行提到 secret 关键字时，把长 hex 也遮掉
	if lineHasSecretKeyword(out) {
		out = redactLongHex(out)
	}

	return out
}

func redactPAT(ln string) string {
	for i := 1; i+1 < len(ln); i++ {
		if ln[i] != '|' {
			continue
		}
		// 前面必须全是数字
		j := i - 1
		for j >= 0 && ln[j] >= '0' && ln[j] <= '9' {
			j--
		}
		if i-j < 2 { // 至少 2 位数字才算 tokenId
			continue
		}
		// 后面必须是 32+ 的字母数字
		k := i + 1
		for k < len(ln) && isAlnum(ln[k]) {
			k++
		}
		if k-(i+1) < 32 {
			continue
		}
		return ln[:i+1] + strings.Repeat("*", 12) + "[token]" + ln[k:]
	}

	return ln
}

func redactLongHex(ln string) string {
	var b strings.Builder
	i := 0
	for i < len(ln) {
		if isHex(ln[i]) {
			j := i
			for j < len(ln) && isHex(ln[j]) {
				j++
			}
			if j-i >= 32 {
				b.WriteString(strings.Repeat("*", 8) + "[hex]")
			} else {
				b.WriteString(ln[i:j])
			}
			i = j
			continue
		}
		b.WriteByte(ln[i])
		i++
	}

	return b.String()
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isAlnum(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func fileNonEmpty(p string) bool {
	st, err := os.Stat(p)

	return err == nil && st.Size() > 0
}

// readKV 读 KEY=VALUE 文件（mynet.conf 的形状）。
func readKV(p string) map[string]string {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	kv := map[string]string{}
	for _, ln := range strings.Split(string(b), "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if k, v, ok := strings.Cut(ln, "="); ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	return kv
}
