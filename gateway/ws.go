package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// 节点面 WS 协议常量。子协议名与 mynetd 侧 transport.WebSocketDialer 必须一致
// （mynetd/transport/dial_websocket.go:22）。
const (
	NodeSubprotocol = "mynet.node.v1"

	// NodeIDHeader 只作**寻址与 presence**（门牌号），不是授权（C-3）。
	NodeIDHeader = "X-Node-Id"

	// 单帧上限。命令帧与 ping 都是小 JSON，1 MiB 已远超需要；
	// 设限是防止异常客户端用巨帧撑爆内存。
	maxFrameBytes = 1 << 20

	writeTimeout = 10 * time.Second
)

// handleNodeConnect 是 /v1/node/connect 的真 WS 握手。
//
// C-3：gateway **不鉴权**。这里不验签名、不查授权——连接即登记，Verified=false
// 直到 back 经 X-Node-Verified 响应头置位（C-4，见 withVerifiedHook）。
// 节点面真鉴权是节点↔back 的 DH+HMAC（C-2），发生在 relay 到 back 之后。
func (s *Server) handleNodeConnect(w http.ResponseWriter, r *http.Request) {
	nodeID := r.Header.Get(NodeIDHeader)
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"message": "X-Node-Id required（寻址用，非授权）",
		})
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{NodeSubprotocol},
		// 不放开 Origin 校验：浏览器发起的跨站 WS 会被拒。
		// 节点侧是 Go 客户端、不带 Origin，不受影响。
	})
	if err != nil {
		// Accept 已写好响应（非升级请求会是 400）。这里只记原因，不再写。
		s.logf("gateway: WS 握手失败 node=%s: %v", nodeID, err)
		return
	}
	conn.SetReadLimit(maxFrameBytes)

	// 握手成功后才登记。ctx 可取消：被同 nodeID 的新连接踢掉时，
	// Register 会 Close 旧 Conn，从而取消这里的 ctx，让下面的 conn.Read 立即返回。
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	_, c := s.reg.RegisterCtx(nodeID, cancel)
	s.logf("gateway: 节点接入 node=%s conns=%d", nodeID, s.reg.Count())

	// 写循环：把 Registry.Notify 投进 send 的帧真正发出去。
	// 没有它，send 缓冲（16）填满后 Notify 一律 errSendFull，推送等于不存在。
	go s.writeLoop(ctx, conn, c)

	defer func() {
		c.Close()
		// UnregisterIf 而非 Unregister：被踢的旧连接收尾时，表里可能已经是新连接，
		// 无条件删会把新连接误摘掉。
		if s.reg.UnregisterIf(nodeID, c) {
			s.logf("gateway: 节点断开 node=%s conns=%d", nodeID, s.reg.Count())
		}
		_ = conn.Close(websocket.StatusNormalClosure, "bye")
	}()

	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil && !isNormalClosure(err) {
				s.logf("gateway: 读结束 node=%s: %v", nodeID, err)
			}
			return
		}
		s.handleNodeFrame(ctx, conn, nodeID, raw)
	}
}

// writeLoop 排空 Conn.send。c.done 或 ctx 结束即退出。
func (s *Server) writeLoop(ctx context.Context, conn *websocket.Conn, c *Conn) {
	for {
		select {
		case <-c.done:
			return
		case <-ctx.Done():
			return
		case payload := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Write(wctx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				s.logf("gateway: 写失败 node=%s: %v", c.NodeID, err)
				// 写不通说明连接已坏；关掉让读循环收尾并摘表。
				c.Close()
				return
			}
		}
	}
}

// handleNodeFrame 处理节点上行帧。
//
// 两类：ping（保活）与 result（命令执行结果，转发给 back）。
func (s *Server) handleNodeFrame(ctx context.Context, conn *websocket.Conn, nodeID string, raw []byte) {
	var probe struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		s.logf("gateway: 帧解析失败 node=%s: %v", nodeID, err)
		return
	}

	switch probe.T {
	case "ping":
		s.writePong(ctx, conn, nodeID)
	case "result":
		s.forwardNodeResult(ctx, nodeID, raw)
	}
}

func (s *Server) writePong(ctx context.Context, conn *websocket.Conn, nodeID string) {
	pong, _ := json.Marshal(map[string]string{"t": "pong", "node_id": nodeID})
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := conn.Write(wctx, websocket.MessageText, pong); err != nil {
		s.logf("gateway: pong 写失败 node=%s: %v", nodeID, err)
	}
}

// nodeResultFrame 节点上行的命令结果帧。
// 与 cmd/mynetd/nodeface.go 的 resultFrame 对称——改一边必须改另一边。
type nodeResultFrame struct {
	T      string `json:"t"`
	NodeID string `json:"node_id"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   string `json:"body"`
	TS     string `json:"ts"`
	Sig    string `json:"sig"`
}

// 节点面上行的窄白名单。
//
// 为什么必须有：path 由节点提供，而 gateway 拿它去拼**内网**上游——这是 SSRF 面。
// 用户面的 relaypolicy 是「业务接口白名单」，语义不同，不能复用：节点面只该能
// 回报自己的命令结果与心跳，别的一律拒。
var nodeUpstreamPatterns = []struct {
	method string
	re     *regexp.Regexp
}{
	{http.MethodPatch, regexp.MustCompile(`^/api/v1/monitor/commands/[0-9A-Za-z_-]+/result$`)},
	{http.MethodPost, regexp.MustCompile(`^/api/v1/monitor/heartbeat$`)},
}

func allowedNodeUpstream(method, path string) bool {
	if strings.Contains(path, "%") { // 与 relaypolicy 同理：防 %2f 绕过
		return false
	}
	for _, p := range nodeUpstreamPatterns {
		if p.method == method && p.re.MatchString(path) {
			return true
		}
	}
	return false
}

// forwardNodeResult 把节点签好名的结果**逐字重放**给 back。
//
// gateway 不做任何密码学（C-3）：X-Node-Id / X-Timestamp / X-Node-Signature 三个头
// 原样带上，验签由 back 的 node.heartbeat.auth 中间件完成。签名覆盖的是
// `METHOD\npath\nts\nsha256(body)`，所以 method/path/body 一个字都不能改。
func (s *Server) forwardNodeResult(ctx context.Context, connNodeID string, raw []byte) {
	var f nodeResultFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		s.logf("gateway: result 帧解析失败 node=%s: %v", connNodeID, err)
		return
	}

	// 帧里的 node_id 必须与连接身份一致，否则一个节点能替别的节点报结果。
	// （back 还会验签，这里是第一道闸，也便于就地定位问题。）
	if f.NodeID != connNodeID {
		s.logf("gateway: result 帧 node_id 不符（帧=%s 连接=%s），丢弃", f.NodeID, connNodeID)
		return
	}
	if !allowedNodeUpstream(f.Method, f.Path) {
		s.logf("gateway: result 目标不在节点面白名单内 method=%s path=%s node=%s，丢弃",
			f.Method, f.Path, connNodeID)
		return
	}
	if s.Upstream == "" {
		s.logf("gateway: 上游未配置，无法转发 result node=%s", connNodeID)
		return
	}
	for _, h := range []struct{ name, val string }{
		{"X-Node-Id", f.NodeID}, {"X-Timestamp", f.TS}, {"X-Node-Signature", f.Sig},
	} {
		if h.val == "" {
			s.logf("gateway: result 帧缺 %s，丢弃 node=%s", h.name, connNodeID)
			return
		}
	}

	target, err := s.resolveUpstream(f.Path)
	if err != nil {
		s.logf("gateway: result 目标解析失败 path=%s: %v", f.Path, err)
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, relayTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, f.Method, target.String(), strings.NewReader(f.Body))
	if err != nil {
		s.logf("gateway: 构造上游请求失败: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Node-Id", f.NodeID)
	req.Header.Set("X-Timestamp", f.TS)
	req.Header.Set("X-Node-Signature", f.Sig)

	resp, err := nodeUpstreamClient.Do(req)
	if err != nil {
		s.logf("gateway: 转发 result 失败 node=%s path=%s: %v", connNodeID, f.Path, err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // 排空以便复用连接

	s.logf("gateway: result 已转发 node=%s path=%s → %d", connNodeID, f.Path, resp.StatusCode)
}

// nodeUpstreamClient 节点面转发专用 client。超时由每请求的 ctx 控制。
var nodeUpstreamClient = &http.Client{}

func isNormalClosure(err error) bool {
	return websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
		websocket.CloseStatus(err) == websocket.StatusGoingAway
}
