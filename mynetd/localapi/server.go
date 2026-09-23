// Package localapi — 本机 API / WebUI 入口（D-1 本地 token · D-2 Origin/Host 校验）。
//
// 浏览器只谈本机（127.0.0.1:8096）；平台态经 gateway 的 /v1/relay。
// GUI 零持有 back 凭据。
package localapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
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
}

type Server struct {
	cfg Config
	mu  sync.Mutex
	tok string
	mux *http.ServeMux
	ln  net.Listener
	srv *http.Server
	// Ops 执行器命令分派（mynet <子命令>），由 mynetd 装配注入。
	Ops func(argv []string) (any, error)
	// sessions R5-1 多节点并行会话
	sessions *SessionTable
}

func NewServer(cfg Config) *Server {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	s := &Server{cfg: cfg, tok: cfg.Token, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/api/v1/status", s.handleStatus)
	s.mux.HandleFunc("/api/v1/system.json", s.handleSystemJSON)
	s.mux.HandleFunc("/api/v1/metrics", s.handleMetrics)
	s.mux.HandleFunc("/api/v1/command", s.handleCommand)
	s.mux.HandleFunc("/api/v1/nodes/switch", s.handleNodesSwitch) // D-10 / R5-1 焦点切换
	s.mux.HandleFunc("/api/v1/nodes", s.handleNodesList)          // R5-1 并行会话
	if hasWebUI {
		s.mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(webUIFS()))))
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
	s.srv = &http.Server{Handler: s.withSecurity(s.mux)}
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
