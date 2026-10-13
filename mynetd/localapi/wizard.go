package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

)

// relayToBack 经 gateway /v1/relay 打后端路径（本机 WebUI 的唯一出网方式）。
func relayToBack(ctx context.Context, method, backPath string, body []byte) ([]byte, int, error) {
	gw := firstGateway()
	if gw == "" {
		return nil, 0, fmt.Errorf("网关未配置（$MYNET_GATEWAYS / conf/gateways.json / conf/agent.json.gateway 均无），无法中继到后端")
	}
	target := strings.TrimRight(gw, "/") + "/v1/relay?path=" + url.QueryEscape(backPath)
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: relayTimeout}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return raw, resp.StatusCode, err
}

func anyToStrSafe(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		// JSON 数字默认进 float64；雪花 ID 用 %v 会变科学计数法（3.84e+15）
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return strings.TrimSpace(strconv.FormatFloat(t, 'f', -1, 64))
	case json.Number:
		return t.String()
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func expiryFromSecs(secs int) string {
	if secs <= 0 {
		return ""
	}
	return time.Now().Add(time.Duration(secs) * time.Second).Format(time.RFC3339)
}

func machineName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "local"
}

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
	// P1 状态机：六态 + 一言之 + 按钮面。与 wizard/state 分开是刻意的 ——
	// wizard/state 回答「向导走到哪」，node-status 回答「这台机器的节点能不能用」。
	s.mux.HandleFunc("/api/v1/node-status", s.handleNodeStatus)
}

// handleWizardLogin —— 本机 WebUI 登录。
//
// 链路：浏览器 → mynetd(127.0.0.1) → gateway /v1/relay → 后端 `/api/v1/auth/login`
// **不**让浏览器/表单填云端 API；api_base 概念对本机页不存在。
// 口令只进请求体（发给后端），**不进 argv、不落日志、不回显**。
// 成功后把会话写进本机 credential.json（0600），供节点面/CLI 复用。
//
//	POST {"email":"…","password":"…"}
func (s *Server) handleWizardLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "POST only"})
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		// 旧字段 api_base_url 已取消：后端地址不下发、不入参（未知键静默忽略）。
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"success": false, "message": "bad json"})
		return
	}
	if body.Email == "" || body.Password == "" {
		writeJSON(w, 400, map[string]any{"success": false, "message": "email / password 必填"})
		return
	}

	payload, _ := json.Marshal(map[string]string{
		"email":              body.Email,
		"password":           body.Password,
		"device_name":        machineName(),
		"device_fingerprint": s.fingerprint(),
	})
	relayFn := s.Relay
	if relayFn == nil {
		relayFn = relayToBack
	}
	raw, code, err := relayFn(r.Context(), "POST", "/api/v1/auth/login", payload)
	if err != nil {
		writeJSON(w, 502, map[string]any{"success": false, "message": "登录转发失败: " + err.Error()})
		return
	}

	var env struct {
		Success bool `json:"success"`
		Data    struct {
			User struct {
				UserID interface{} `json:"user_id"`
				Name   string      `json:"name"`
				Email  string      `json:"email"`
			} `json:"user"`
			TenantID           interface{} `json:"tenant_id"`
			AuthToken          string      `json:"auth_token"`
			RefreshToken       string      `json:"refresh_token"`
			AuthTokenExpiresIn int         `json:"auth_token_expires_in"`
			RefreshExpiresIn   int         `json:"refresh_token_expires_in"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		writeJSON(w, 502, map[string]any{"success": false, "message": "登录响应无法解析"})
		return
	}
	if !env.Success || env.Data.AuthToken == "" {
		msg := env.Message
		if msg == "" {
			msg = fmt.Sprintf("登录失败（HTTP %d）", code)
		}
		writeJSON(w, code, map[string]any{"success": false, "message": msg})
		return
	}

	// 会话落盘（0600）：token 不回浏览器
	sess := LoginSession{
		UserID:            anyToStrSafe(env.Data.User.UserID),
		UserEmail:         env.Data.User.Email,
		Token:             env.Data.AuthToken,
		RefreshToken:      env.Data.RefreshToken,
		AuthExpiresAt:     expiryFromSecs(env.Data.AuthTokenExpiresIn),
		RefreshExpiresAt:  expiryFromSecs(env.Data.RefreshExpiresIn),
		TenantID:          anyToStrSafe(env.Data.TenantID),
		MachineID:         machineName(),
		DeviceFingerprint: s.fingerprint(),
		CreatedAt:         time.Now().UTC().Format(time.RFC3339),
	}
	save := s.SaveLoginSession
	if save == nil {
		writeJSON(w, 500, map[string]any{"success": false, "message": "SaveLoginSession 未注入"})
		return
	}
	if err := save(sess); err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "message": "会话保存失败: " + err.Error()})
		return
	}

	writeJSON(w, 200, map[string]any{
		"success": true,
		"data": map[string]any{
			"logged_in":  true,
			"user_email": sess.UserEmail,
			"user_id":    sess.UserID,
			"tenant_id":  sess.TenantID,
		},
	})
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
	// 运行中的身份以 active profile 为准：mynet.conf 可能残留切换/迁移前的旧 NODE_ID，
	// 两处不一致时 UI 会「一边成功一边未配置」。
	if data, err := s.opsJSON([]string{"config", "identity", "--json"}); err == nil && data != nil {
		if id, _ := data["node_id"].(string); id != "" {
			st["node_id"] = id
		}
		if nm, _ := data["tenant_name"].(string); nm != "" {
			st["tenant_name"] = nm
		}
		if em, _ := data["user_email"].(string); em != "" {
			st["user_email"] = em
		}
	}
	// 从 credential.json 只取**非敏感**字段
	if b, err := os.ReadFile(filepath.Join(conf, "credential.json")); err == nil {
		var c map[string]any
		if json.Unmarshal(b, &c) == nil {
			st["user_email"] = c["user_email"]
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
			// argv 会原样进浏览器（webui 的 cmd-out <pre>）—— **必须遮蔽**
			// secret 类参数的值（--private/--password/…，C1-S1）：
			// 私钥曾从这里回显。email 等非秘密参数保留可见，用户要看。
			out[k] = redactArgvAny(v)
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
// ⚠️ 第一版规则是「32+ 的 hex 一律遮」，实测把后端的**节点面公钥**也遮成了
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

// secretArgKeywords —— argv 里**值是秘密**的参数名关键词（小写，包含匹配）。
// 与 secretKeywords 同源，但不含 "refresh"（`--refresh` 类开关不带值，遮了会吞参数）。
var secretArgKeywords = []string{"priv", "private", "password", "secret", "token"}

// redactArgv 遮蔽 argv 里的秘密值：`--private`/`--password`/`--token`/`--secret`
// 类参数的**值**（`--flag value` 与 `--flag=value` 两种形态）。成功与失败路径都要走 ——
// argv 会进 HTTP 响应回显面（浏览器可见）。`*-stdin` 开关本身不带值，不吞下一个参数。
//
// 私钥/口令进 `ps` 的问题靠 `--private-stdin` 根治（config_api / parseKeysSetArgs），
// 这里兜的是「旧调用方仍把秘密摆进 argv」的残余形态。
func redactArgv(argv []string) []string {
	const mask = "********"
	out := append([]string(nil), argv...)
	for i := 0; i < len(out); i++ {
		a := out[i]
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, hasEq := strings.Cut(a, "=")
		low := strings.ToLower(name)
		if strings.HasSuffix(low, "-stdin") {
			continue
		}
		if !argvFlagHasSecretKeyword(low) {
			continue
		}
		if hasEq {
			out[i] = name + "=" + mask
		} else if i+1 < len(out) && !strings.HasPrefix(out[i+1], "-") {
			out[i+1] = mask
			i++
		}
	}

	return out
}

func argvFlagHasSecretKeyword(flag string) bool {
	for _, k := range secretArgKeywords {
		if strings.Contains(flag, k) {
			return true
		}
	}

	return false
}

// secretArgInArgv 检查 argv 是否带**秘密直传**参数（`--private <值>` /
// `--private=值` / `--password` / `--token` / `--secret` 类），返回首个违规
// 参数名（不含值，报错也不回显值）。`*-stdin` 开关不算 —— 那是收口后的
// 正道（G1-c：秘密只经 stdin）。判据与 redactArgv 同源（secretArgKeywords）。
//
// S1 残余：回显遮蔽只解决「响应里看得见」，进 ps 这条要靠**拒绝直传**——
// 值摆上 argv 的那一刻就已经泄露给同机用户了。
func secretArgInArgv(argv []string) string {
	for _, a := range argv {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, _ := strings.Cut(a, "=")
		low := strings.ToLower(name)
		if strings.HasSuffix(low, "-stdin") {
			continue
		}
		if argvFlagHasSecretKeyword(low) {
			return name
		}
	}

	return ""
}

// redactArgvAny 把 any 形态的 argv（[]string 或 JSON 反序列化来的 []any）遮蔽后返回。
func redactArgvAny(v any) any {
	switch t := v.(type) {
	case []string:
		return redactArgv(t)
	case []any:
		strs := make([]string, len(t))
		for i, e := range t {
			strs[i], _ = e.(string)
		}
		masked := redactArgv(strs)
		out := make([]any, len(masked))
		for i, s := range masked {
			out[i] = s
		}
		return out
	}

	return v
}

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

// fingerprint 取设备指纹；未注入时返回空（组合根应注入 mynet.DeviceFingerprint）。
func (s *Server) fingerprint() string {
	if s.DeviceFingerprint != nil {
		return s.DeviceFingerprint()
	}
	return ""
}
