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
	cancel context.CancelFunc
	// watchCancel 取消该连接的 presence 订阅（跨实例事件的收货口）。
	// 不挂在 cancel 上是因为 cancel 归调用方（WS handler），而订阅是 Registry
	// 自己起的；两者要一起在 Close 时收掉。
	watchCancel context.CancelFunc
	closeOnce   sync.Once
}

// Close 幂等地结束该连接：停 writeLoop（done）+ 取消读循环（cancel）+ 退订（watch）。
// 踢旧连接、写失败、读结束都走这里。
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.cancel != nil {
			c.cancel()
		}
		if c.watchCancel != nil {
			c.watchCancel()
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
	ps := r.presence
	r.mu.Unlock()
	if old != nil {
		old.Close()
	}
	if ps != nil {
		// 连上就上报 online（verified 尚未置位，等 back 的 C-4）。
		// 原实现只在 MarkVerified 里 SetOnline —— 断开时也没人 SetOffline，
		// 于是 Redis 里会残留「这个节点还在线」的状态，直到被同 ID 节点重连覆盖。
		_ = ps.SetOnline(context.Background(), nodeID, false)
	}
	// 订阅跨实例事件：节点连在**本实例**，而 back 可能通知到**别的**实例
	// —— 那个实例发现本地无连接，经 Redis PUBLISH 把帧丢过来，由这里收货。
	watchCtx, watchCancel := context.WithCancel(context.Background())
	c.watchCancel = watchCancel
	r.watchNode(watchCtx, c)
	return old, c
}

// watchNode 消费该连接的 presence 订阅帧，并投递给本地连接。
//
// 只投给**本地**：跨实例的广播是「谁有连接谁发」，所以不会重复推送；
// 也不会自己发给自己（Notify 在本地有连接时直接投递、根本不 Publish）。
func (r *Registry) watchNode(ctx context.Context, target *Conn) {
	r.mu.RLock()
	ps := r.presence
	r.mu.RUnlock()
	if ps == nil {
		return
	}
	ch, err := ps.Subscribe(ctx, target.NodeID)
	if err != nil {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case frame, ok := <-ch:
				if !ok {
					return
				}
				// 确认目标连接仍是它（可能已被新连接顶替）
				r.mu.RLock()
				cur := r.conns[target.NodeID]
				r.mu.RUnlock()
				if cur != target {
					return
				}
				if !target.Verified {
					continue // C-5：未置位不推
				}
				select {
				case target.send <- frame:
				default:
					// 队列满丢弃：与 Notify 的 errSendFull 同语义
				}
			}
		}
	}()
}

func (r *Registry) Unregister(nodeID string) {
	r.mu.Lock()
	delete(r.conns, nodeID)
	ps := r.presence
	r.mu.Unlock()
	if ps != nil {
		// 断开要落 offline，否则 Redis 里这个节点会一直显示在线
		// （原实现只在 MarkVerified 里 SetOnline，断开时没人管）
		_ = ps.SetOffline(context.Background(), nodeID)
	}
}

// UnregisterIf 仅当该 nodeID 仍指向 c 时才摘除。
//
// 必须的：节点重连时新连接先 Register（旧连接被踢），旧连接的收尾 defer 才跑。
// 无条件 Unregister 会把刚登记的新连接误摘掉，表现为「节点明明连着却显示离线」。
func (r *Registry) UnregisterIf(nodeID string, c *Conn) bool {
	r.mu.Lock()
	if cur, ok := r.conns[nodeID]; !ok || cur != c {
		r.mu.Unlock()
		return false
	}
	delete(r.conns, nodeID)
	ps := r.presence
	r.mu.Unlock()
	if ps != nil {
		_ = ps.SetOffline(context.Background(), nodeID)
	}
	return true
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

// Notify 推事件给节点（back → gateway → 节点，C-5 只推已置位的连接）。
//
// 多实例（V4-5）：
//  1. 节点连在**本实例** → 直接投递，结束（也避免了自己 Publish 自己收的重复）
//  2. 节点连在**别的实例**（本地无连接，但 presence 显示在线且已置位）
//     → 经 presence.Publish 把**帧原文**广播过去，对方的 watchNode 收货并投递
//  3. 确实离线 → errNotOnline（这是唯一该回 404 的情形）
//
// 原实现只查本地 conns，所以节点连 A 时 back 通知 B 一律 404 ——
// 判据里那句「现在必 404」就是这么来的。
func (r *Registry) Notify(ctx context.Context, nodeID, event string, data map[string]string) error {
	payload, _ := json.Marshal(map[string]any{"event": event, "data": data})

	r.mu.RLock()
	c, ok := r.conns[nodeID]
	ps := r.presence
	r.mu.RUnlock()

	if ok {
		if !c.Verified {
			return errNotVerified
		}
		select {
		case c.send <- payload:
			return nil
		default:
			return errSendFull
		}
	}

	// 本地没有 → 看看是不是连在别的实例上
	if ps != nil {
		online, verified, err := ps.IsOnline(ctx, nodeID)
		if err == nil && online && verified {
			return ps.Publish(ctx, nodeID, payload)
		}
	}
	return errNotOnline
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
	if err := s.reg.Notify(r.Context(), req.NodeID, req.Event, req.Data); err != nil {
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
