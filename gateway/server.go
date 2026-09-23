// Package gateway — 边缘网关：WS（节点面）+ HTTPS（用户面 /v1/relay）。
//
// **gateway 不鉴权**（C-3）——纯透传 + 连接状态表（像 TCP 握手）。
// 连接层的节点标识只做**寻址与 presence**（门牌号），不是授权。
// 鉴权一律归 back：用户面 cookie/会话，节点面 DH+HMAC。
//
// C-4：back 验证通过任意一次节点通道请求后，在响应头写
//
//	X-Node-Verified: <node_id>
//
// gateway 解析后把该连接标为「已鉴权」；伪造 node_id 不被置位。
package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// VerifiedHeader back → gateway 的置位信号（C-4）
const VerifiedHeader = "X-Node-Verified"

// Conn 是一条已接受的节点 WS 连接的状态。
type Conn struct {
	NodeID    string
	Connected time.Time
	// Verified 是否已被 back 验证过至少一次（C-4）
	Verified bool
	// 简化：真实 WS 句柄由 ws.go 持有
	send chan []byte
}

// Registry 连接状态表 + presence（内存版；C-6 换 Redis pub/sub）
type Registry struct {
	mu       sync.RWMutex
	conns    map[string]*Conn
	presence PresenceStore
}

func NewRegistry() *Registry {
	return &Registry{conns: map[string]*Conn{}, presence: NewMemoryPresence()}
}

// SetPresence 注入 Redis/内存 presence（C-6）
func (r *Registry) SetPresence(p PresenceStore) {
	r.mu.Lock()
	r.presence = p
	r.mu.Unlock()
}

// Register 登记连接；同 nodeID 踢旧连接并返回它。
func (r *Registry) Register(nodeID string) (evicted *Conn, c *Conn) {
	c = &Conn{NodeID: nodeID, Connected: time.Now(), send: make(chan []byte, 16)}
	r.mu.Lock()
	old := r.conns[nodeID]
	r.conns[nodeID] = c
	r.mu.Unlock()
	return old, c
}

func (r *Registry) Unregister(nodeID string) {
	r.mu.Lock()
	delete(r.conns, nodeID)
	r.mu.Unlock()
}

func (r *Registry) Get(nodeID string) (*Conn, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.conns[nodeID]
	return c, ok
}

func (r *Registry) IsOnline(nodeID string) bool {
	_, ok := r.Get(nodeID)
	return ok
}

// MarkVerified C-4：back 置位「已鉴权」
func (r *Registry) MarkVerified(nodeID string) bool {
	r.mu.Lock()
	c, ok := r.conns[nodeID]
	if ok {
		c.Verified = true
	}
	ps := r.presence
	r.mu.Unlock()
	if ok && ps != nil {
		_ = ps.SetOnline(context.Background(), nodeID, true)
	}
	return ok
}

// IsVerified 只有置位的连接才收推送（C-5）
func (r *Registry) IsVerified(nodeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.conns[nodeID]
	return ok && c.Verified
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.conns)
}

// Notify 只推给**已置位**的连接（C-5）
func (r *Registry) Notify(nodeID, event string, data map[string]string) error {
	r.mu.RLock()
	c, ok := r.conns[nodeID]
	verified := ok && c.Verified
	r.mu.RUnlock()
	if !ok {
		return errNotOnline
	}
	if !verified {
		return errNotVerified
	}
	payload, _ := json.Marshal(map[string]any{"event": event, "data": data})
	select {
	case c.send <- payload:
		return nil
	default:
		return errSendFull
	}
}

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

const (
	errNotOnline   = simpleErr("node not online")
	errNotVerified = simpleErr("node not verified")
	errSendFull    = simpleErr("send buffer full")
)

// Server HTTP 入口
type Server struct {
	reg *Registry
	// Upstream back 的内网地址（部署配置，不下发）
	Upstream string
	// InternalToken back ↔ gateway 的运维鉴权（**不是**节点鉴权）
	InternalToken string
}

func NewServer(upstream, internalToken string) *Server {
	return &Server{reg: NewRegistry(), Upstream: upstream, InternalToken: internalToken}
}

func (s *Server) Registry() *Registry { return s.reg }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/node/connect", s.handleConnect)
	mux.HandleFunc("/v1/presence/", s.handlePresence)
	mux.HandleFunc("/v1/notify", s.handleNotify)
	mux.HandleFunc("/v1/relay", s.handleRelay) // C-7 用户面入口
	return s.withVerifiedHook(mux)
}

// withVerifiedHook 解析 back 响应头 X-Node-Verified 并置位（C-4）
func (s *Server) withVerifiedHook(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &verifiedRecorder{ResponseWriter: w}
		next.ServeHTTP(rw, r)
		if rw.nodeID != "" {
			s.reg.MarkVerified(rw.nodeID)
		}
	})
}

type verifiedRecorder struct {
	http.ResponseWriter
	nodeID string
}

func (v *verifiedRecorder) WriteHeader(code int) {
	if h := v.Header().Get(VerifiedHeader); h != "" {
		v.nodeID = h
	}
	v.ResponseWriter.WriteHeader(code)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"success": true, "data": map[string]any{"conns": s.reg.Count()}})
}

func (s *Server) handlePresence(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimPrefix(r.URL.Path, "/v1/presence/")
	// presence 只答状态；推送只给已置位连接（C-5）
	writeJSON(w, 200, map[string]any{
		"success": true,
		"data": map[string]any{
			"node_id":  nodeID,
			"online":   s.reg.IsOnline(nodeID),
			"verified": s.reg.IsVerified(nodeID),
		},
	})
}

// notifyRequest back → gateway 推送指令
type notifyRequest struct {
	NodeID string            `json:"node_id"`
	Event  string            `json:"event"`
	Data   map[string]string `json:"data,omitempty"`
}

func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "POST only"})
		return
	}
	// 内部运维口令（非节点鉴权）
	if s.InternalToken != "" && r.Header.Get("X-Internal-Token") != s.InternalToken {
		writeJSON(w, 401, map[string]any{"success": false, "message": "internal token required"})
		return
	}
	var req notifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NodeID == "" {
		writeJSON(w, 400, map[string]any{"success": false, "message": "node_id required"})
		return
	}
	if err := s.reg.Notify(req.NodeID, req.Event, req.Data); err != nil {
		code := 404
		if err == errNotVerified {
			code = 403
		}
		writeJSON(w, code, map[string]any{"success": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

// handleRelay C-7 用户面 HTTPS 入口：无身份，鉴权归 back
func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	// 仅路径白名单（relaypolicy）；此处不鉴权
	writeJSON(w, 502, map[string]any{
		"success": false,
		"message": "relay 上游未配置或不可达；鉴权归 back",
	})
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	// 真实 WebSocket 握手在 ws.go；此处占位说明协议
	nodeID := r.Header.Get("X-Node-Id")
	if nodeID == "" {
		writeJSON(w, 400, map[string]any{"success": false, "message": "X-Node-Id required（寻址用）"})
		return
	}
	// C-3：不做签名验证。连接即登记；Verified=false 直到 back 置位。
	_, c := s.reg.Register(nodeID)
	writeJSON(w, 200, map[string]any{
		"success": true,
		"data": map[string]any{
			"node_id":  nodeID,
			"verified": c.Verified,
			"note":     "gateway 不鉴权；授权归 back",
		},
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
