// Package localapi — 本机 API / WebUI 入口（D-1 本地 token · D-2 Origin/Host 校验）。
//
// 浏览器只谈本机（127.0.0.1:8096）；平台态经 gateway 的 /v1/relay。
// GUI 零持有 back 凭据。
package localapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/logger"
)

const DefaultListen = "127.0.0.1:8096"

// Cookie 名与属性（D-1）：HttpOnly + SameSite=Strict，防 XSS 读取与 CSRF。
const AuthCookieName = "mynet_local"

type Config struct {
	Listen string
	// AllowedHosts 允许的 Host 头（D-2 防 DNS rebinding）。空 = 仅 localhost/127.0.0.1/[::1]。
	AllowedHosts []string
	// Token 本地 token；空则首次 GET / 时生成并 Set-Cookie。
	Token string
	// ProxyConfDir 双控状态目录（proxy_intent.json）。空则 conf/local。
	ProxyConfDir string
}

type Server struct {
	cfg            Config
	mu             sync.Mutex
	configMutation sync.Mutex
	tok            string
	mux            *http.ServeMux
	ln             net.Listener
	srv            *http.Server
	// Ops 执行器命令分派（mynet <子命令>），由 mynetd 装配注入。
	Ops func(argv []string) (any, error)
	// Relay 向导登录等平台调用：经 gateway 打 back。可注入便于测试。
	Relay func(ctx context.Context, method, backPath string, body []byte) ([]byte, int, error)

	// ExecStdin 向导登录专用：能喂 stdin 的执行器。口令不走 argv ——
	// argv 会进 `ps`，任何本机进程都看得见。与 Ops 同为可注入，
	// 为的是让「口令不进 argv」这条安全性质**可测**，而不是靠肉眼看代码确认。
	ExecStdin func(argv []string, stdin string) (map[string]any, error)
	// ProxyWriteback 本地改 proxy 后回写云端（node-face → gateway → back）。
	ProxyWriteback ProxyWritebackFunc
	// Proxy 意图读写 / 插件投影（组合根注入，localapi 不 import shared/config / mynet —— B2）
	ProxyLoad       ProxyIntentLoader
	ProxySave       ProxyIntentSaver
	ProxySyncPlugin func(p *domain.ProxyPolicy) error
	// sessions R5-1 多节点并行会话
	sessions *SessionTable
	// OnResult E-4：执行结果回调（经 WS 上报）
	OnResult func(id string, ok bool, message, source string)
}

func NewServer(cfg Config) *Server {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	s := &Server{cfg: cfg, tok: cfg.Token, mux: http.NewServeMux(), sessions: NewSessionTable(), Ops: defaultExecMynet}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/api/v1/status", s.handleStatus)
	s.mux.HandleFunc("/api/v1/system.json", s.handleSystemJSON)
	s.mux.HandleFunc("/api/v1/logs", s.handleLogs)
	s.mux.HandleFunc("/api/v1/metrics", s.handleMetrics)
	s.mux.HandleFunc("/api/v1/command", s.handleCommand)
	s.mux.HandleFunc("/api/v1/command-result", s.handleCommandResult)                   // E-4
	s.mux.HandleFunc("/api/v1/nodes/switch", s.withConfigMutation(s.handleNodesSwitch)) // D-10 / R5-1 焦点切换
	s.mux.HandleFunc("/api/v1/nodes", s.handleNodesList)                                // R5-1 并行会话
	s.mux.HandleFunc("/api/v1/relay", s.handleRelay)                                    // GUI→mynetd→gateway→back 平台面
	s.mux.HandleFunc("/gui/", s.handleGUI)                                              // mynet_gui 控制台（API 走 relay）
	// 向导端点是 **API 不是 UI**，不随 embed 构建标签开合：
	// 没有内嵌 webui 的构建（D-11 的 OpenWrt 形态）照样要能被 LuCI/脚本调用。
	s.registerWizardRoutes()
	s.registerConfigRoutes()
	s.registerProxyRoutes()
	if hasWebUI {
		uiFiles := http.StripPrefix("/ui/", http.FileServer(http.FS(webUIFS())))
		s.mux.Handle("/ui/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// WebUI is embedded in the daemon; never let an older index/script stay cached after an upgrade.
			w.Header().Set("Cache-Control", "no-store, max-age=0")
			uiFiles.ServeHTTP(w, r)
		}))
	}
}

// handleNodesList R5-1：并行会话表
func (s *Server) handleNodesList(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		s.sessions = NewSessionTable()
	}
	writeJSON(w, 200, map[string]any{
		"success": true,
		"data": map[string]any{
			"focus":    s.sessions.Focused(),
			"parallel": s.sessions.ParallelCount(),
			"nodes":    s.sessions.List(),
		},
	})
}

// handleNodesSwitch D-10/R5-1：切换 UI **焦点**；并行会话不互踢。
func (s *Server) handleNodesSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "POST only"})
		return
	}
	var body struct {
		NodeID string `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.NodeID == "" {
		writeJSON(w, 400, map[string]any{"success": false, "message": "node_id required"})
		return
	}
	if s.sessions == nil {
		s.sessions = NewSessionTable()
	}
	s.sessions.Attach(body.NodeID, "")
	ok := s.sessions.Focus(body.NodeID)
	writeJSON(w, 200, map[string]any{
		"success": ok,
		"data": map[string]any{
			"focus":    s.sessions.Focused(),
			"parallel": s.sessions.ParallelCount(),
			"switched": body.NodeID,
		},
	})
}

// Start 监听（:0 时由内核分配端口）
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	s.ln = ln
	s.srv = &http.Server{Handler: s.withAccessLog(s.withSecurity(s.mux))}
	go func() { _ = s.srv.Serve(ln) }()
	return nil
}

func (s *Server) Addr() string {
	if s.ln == nil {
		return s.cfg.Listen
	}
	return s.ln.Addr().String()
}

func (s *Server) Close() error {
	if s.srv != nil {
		return s.srv.Close()
	}
	return nil
}

// withAccessLog —— 本机 API 访问日志（组件 localapi）。
//
// 只记 method/path/status/duration，**不记 body / Authorization / Cookie**。
// 静态资源（/ui/ /gui/）降噪为 debug，命令与配置面记 info。
func (s *Server) withAccessLog(next http.Handler) http.Handler {
	log := logger.Comp("localapi")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		dur := time.Since(start)
		path := r.URL.Path
		if q := r.URL.RawQuery; q != "" && strings.HasPrefix(path, "/api/v1/relay") {
			// relay 的 path 参数是业务路径，值得记；其余 query 不记（可能带 token）
			if p := r.URL.Query().Get("path"); p != "" {
				path += "?path=" + p
			}
		}
		static := strings.HasPrefix(path, "/ui/") || strings.HasPrefix(path, "/gui/")
		if static {
			log.Debug("http", "method", r.Method, "path", path, "status", sw.status, "ms", dur.Milliseconds())
			return
		}
		lvl := "INFO"
		if sw.status >= 500 {
			lvl = "ERROR"
		} else if sw.status >= 400 {
			lvl = "WARN"
		}
		logger.Event("localapi", lvl, "http",
			"method", r.Method, "path", path, "status", sw.status, "ms", dur.Milliseconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// withSecurity — D-2 Origin/Host 校验 + D-1 cookie 种入
func (s *Server) withSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.checkHost(r) {
			http.Error(w, `{"success":false,"message":"invalid host"}`, http.StatusForbidden)
			return
		}
		if !s.checkOrigin(r) {
			http.Error(w, `{"success":false,"message":"invalid origin"}`, http.StatusForbidden)
			return
		}
		s.ensureToken(w, r)
		next.ServeHTTP(w, r)
	})
}

// checkHost 防 DNS rebinding：Host 必须是回环或显式白名单。
func (s *Server) checkHost(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	for _, a := range s.cfg.AllowedHosts {
		if strings.EqualFold(a, host) {
			return true
		}
	}
	return false
}

// checkOrigin：有 Origin 头时必须与 Host 同源且是回环；无 Origin（curl/本机工具）放行。
func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	// 允许 http://127.0.0.1:8096 / http://localhost:8096 等同源
	if strings.HasPrefix(origin, "http://127.0.0.1") ||
		strings.HasPrefix(origin, "http://localhost") ||
		strings.HasPrefix(origin, "http://[::1]") {
		return true
	}
	for _, a := range s.cfg.AllowedHosts {
		if a != "" && strings.Contains(strings.ToLower(origin), strings.ToLower(a)) {
			return true
		}
	}
	return false
}

// ensureToken 首次 GET / 种 HttpOnly+SameSite=Strict cookie（D-1）
func (s *Server) ensureToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.tok == "" {
		s.tok = newToken()
	}
	tok := s.tok
	s.mu.Unlock()

	if c, err := r.Cookie(AuthCookieName); err == nil && c.Value == tok {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     AuthCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   false, // 仅回环 http
		MaxAge:   0,     // 会话 cookie
	})
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Token 供调试/测试读取
func (s *Server) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tok
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// 浏览器要的是面板，不是 JSON 壳：有 WebUI 就进 /ui/。
	// API 探针用 Accept: application/json 或直接打 /api/v1/status。
	accept := r.Header.Get("Accept")
	if hasWebUI && strings.Contains(accept, "text/html") {
		http.Redirect(w, r, "/ui/", http.StatusFound)
		return
	}
	writeJSON(w, 200, map[string]any{
		"success": true,
		"data": map[string]any{
			"name":    "mynetd",
			"local":   s.Addr(),
			"time":    time.Now().UTC(),
			"has_web": hasWebUI,
		},
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"success": true,
		"data": map[string]any{
			"daemon": "mynetd",
			"listen": s.Addr(),
			"role":   "transport-only",
		},
	})
}
