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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
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
	// send 是下行帧队列，由 ws.go 的 writeLoop 排空。
	// 没有 writeLoop 时它只是个 16 格的缓冲区，填满后 Notify 一律 errSendFull。
	send chan []byte
	// done 关闭即通知 writeLoop 退出。由 Close() 幂等关闭。
	done chan struct{}
	// cancel 取消该连接的读循环 ctx。构造时注入、之后只读，故无需加锁。
	// 没有它，被踢的旧连接会永久阻塞在 conn.Read 上 —— goroutine 泄漏。
	cancel    context.CancelFunc
	closeOnce sync.Once
}

// Close 幂等地结束该连接：停 writeLoop（done）+ 取消读循环（cancel）。
// 踢旧连接、写失败、读结束都走这里。
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.cancel != nil {
			c.cancel()
		}
	})
}

// Done 暴露结束信号，供 writeLoop 与测试使用。
func (c *Conn) Done() <-chan struct{} { return c.done }

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

// Register 登记连接（无读循环取消能力的简化入口，供测试与内部使用）。
func (r *Registry) Register(nodeID string) (evicted *Conn, c *Conn) {
	return r.RegisterCtx(nodeID, nil)
}

// RegisterCtx 登记连接；同 nodeID 的旧连接被踢并**真的关闭**。
//
// cancel 由调用方（WS handler）提供，用于在连接被踢时取消其读循环 ctx。
// 关闭旧连接是必须的：它的 writeLoop 阻塞在 select、读循环阻塞在 conn.Read，
// 只把它从表里摘掉会让两个 goroutine 永久泄漏。
func (r *Registry) RegisterCtx(nodeID string, cancel context.CancelFunc) (evicted *Conn, c *Conn) {
	c = &Conn{
		NodeID:    nodeID,
		Connected: time.Now(),
		send:      make(chan []byte, 16),
		done:      make(chan struct{}),
		cancel:    cancel,
	}
	r.mu.Lock()
	old := r.conns[nodeID]
	r.conns[nodeID] = c
	r.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return old, c
}

func (r *Registry) Unregister(nodeID string) {
	r.mu.Lock()
	delete(r.conns, nodeID)
	r.mu.Unlock()
}

// UnregisterIf 仅当该 nodeID 仍指向 c 时才摘除。
//
// 必须的：节点重连时新连接先 Register（旧连接被踢），旧连接的收尾 defer 才跑。
// 无条件 Unregister 会把刚登记的新连接误摘掉，表现为「节点明明连着却显示离线」。
func (r *Registry) UnregisterIf(nodeID string, c *Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.conns[nodeID]; ok && cur == c {
		delete(r.conns, nodeID)
		return true
	}
	return false
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
	// logf 日志出口，默认丢弃；由 cmd/mynet_gateway 注入 stderr。
	logf func(format string, args ...any)
}

func NewServer(upstream, internalToken string) *Server {
	return &Server{
		reg:           NewRegistry(),
		Upstream:      upstream,
		InternalToken: internalToken,
		logf:          func(string, ...any) {},
	}
}

// SetLogger 注入日志出口。
func (s *Server) SetLogger(f func(format string, args ...any)) {
	if f != nil {
		s.logf = f
	}
}

func (s *Server) Registry() *Registry { return s.reg }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/node/connect", s.handleNodeConnect)
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

// Hijack 透传给底层 ResponseWriter。
//
// 必须的：/v1/node/connect 要做 WebSocket 升级，而 websocket.Accept 依赖
// http.Hijacker 接管 TCP 连接。包装一层却不透传 Hijack，升级会直接失败并返回
// 501 Not Implemented —— 且只在真握手时才暴露，用 httptest.NewRecorder 测不出来。
func (v *verifiedRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := v.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("gateway: 底层 ResponseWriter 不支持 Hijack，无法升级 WebSocket")
	}
	return hj.Hijack()
}

// Flush 同理透传，供 SSE/流式响应使用。
func (v *verifiedRecorder) Flush() {
	if f, ok := v.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
