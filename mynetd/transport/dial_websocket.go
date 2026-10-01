package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// resultWriteTimeout 回传一帧命令结果的写超时。结果帧是小 JSON，
// 给足余量但不无限等——写不动说明连接已坏，应交回重连循环。
const resultWriteTimeout = 15 * time.Second

// WebSocketDialer 真 WS 拨号（C-3：只带 X-Node-Id，无签名）。
//
// ⚠️ 「无签名」指的是**这条 WS 连接本身**不做密码学握手（gateway 不鉴权，
// 连接层标识只作寻址与 presence）。命令结果帧里携带的节点面签名是
// node ↔ back 的 C-2 凭证，由上层注入、gateway 逐字透传，与本层无关。
func WebSocketDialer(ctx context.Context, c *Client) error {
	base := c.cfg.Gateways[0]
	h := c.ConnectHeaders()
	conn, _, err := websocket.Dial(ctx, base, &websocket.DialOptions{
		HTTPClient: &http.Client{Timeout: c.cfg.HandshakeTimeout},
		HTTPHeader: http.Header{
			"X-Node-Id": h.Values("X-Node-Id"),
		},
		Subprotocols: []string{"mynet.node.v1"},
	})
	if err != nil {
		return fmt.Errorf("transport dial %s: %w", base, err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	c.logf("transport: connected %s as node %s", base, c.cfg.NodeID)

	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 写锁：ping 循环、下行帧的应答、事件驱动的主动上行可能并发写同一条连接。
	var writeMu sync.Mutex
	write := func(payload []byte) error {
		wctx, wcancel := context.WithTimeout(readCtx, resultWriteTimeout)
		defer wcancel()
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.Write(wctx, websocket.MessageText, payload)
	}

	// 注入上行通道；断开时清空，避免 Send 写到已死的连接上。
	c.setSender(write)
	defer c.setSender(nil)

	// 连上就补一次（见 Handlers.OnConnect）：断线窗口里 back 推的事件不会补发，
	// 靠这次补拉把配置追平。
	c.fireOnConnect(readCtx)

	// 存活证据：pong 是主要来源，但任何下行帧都同样证明「这条连接还活着」，
	// 所以统一记 lastAlive —— 否则繁忙期只刷事件帧、pong 偶发丢失也会误判断开。
	var lastAlive atomic.Int64
	lastAlive.Store(time.Now().UnixNano())

	// 读循环：命令帧 → HandleFrame → 结果帧回写
	go func() {
		defer cancel()
		for {
			_, raw, err := conn.Read(readCtx)
			if err != nil {
				c.logf("transport: read end: %v", err)
				return
			}
			// pong 是传输层控制帧（我们自己每 30s ping 一次的回应），
			// 不属于业务帧，不能喂给 HandleFrame——否则每次 ping 都会刷一条
			// 「无法识别的帧」的错误日志，把真问题淹掉。
			lastAlive.Store(time.Now().UnixNano())
			if isPong(raw) {
				continue
			}
			reply, err := c.HandleFrame(readCtx, raw)
			if err != nil {
				c.logf("transport: handle frame: %v", err)
				continue
			}
			if len(reply) == 0 {
				continue
			}
			// 结果必须回传：此前 HandleFrame 把结果丢弃，back 侧命令永远停在「已下发」，
			// 表现为「命令发了没反应」而不是「执行失败」，极难定位。
			if err := write(reply); err != nil {
				c.logf("transport: 回传命令结果失败: %v", err)
				return
			}
			c.logf("transport: 已回传命令结果（%d 字节）", len(reply))
		}
	}()

	// ping + 判活同一个 tick：间隔必须显著小于 IdleTimeout，否则判活要等下个 tick。
	tick := time.NewTicker(c.cfg.PingInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-readCtx.Done():
			return fmt.Errorf("read loop ended")
		case <-tick.C:
			// 连接可能「看起来在、其实已死」：中间设备（NAT/负载均衡）静默丢包时
			// TCP 不发 FIN，conn.Read 会一直阻塞，重连循环永远不触发 —— 于是节点
			// 静默收不到任何下发，而日志里全是「connected」。pong 超时就是这个
			// 状态的唯一可见证据，超时即主动断开，交给 Run 重连（重连会补拉）。
			if idle := time.Since(time.Unix(0, lastAlive.Load())); idle > c.cfg.IdleTimeout {
				return fmt.Errorf("transport: %s 未收到 pong，判定连接已死并重连", idle)
			}
			ping, _ := json.Marshal(map[string]string{"t": "ping", "node_id": c.cfg.NodeID})
			if err := write(ping); err != nil {
				return err
			}
		}
	}
}

// isPong 判断是否为网关的 pong 控制帧（gateway/ws.go 的 writePong 发
// `{"t":"pong","node_id":…}`）。只认 t 字段，不做完整解析——控制帧应当廉价。
func isPong(raw []byte) bool {
	var f struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return false
	}
	return f.T == "pong"
}

// AutoDialer 平台默认：WS 优先
func AutoDialer() Dialer { return WebSocketDialer }
