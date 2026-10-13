// Package transport — mynetd → gateway 的 WS 长连接（S3-1）。
//
// 设计（C-3/C-4）：gateway **不鉴权**，只做透传 + 连接状态表。
// 连接时带 X-Node-Id 仅作**寻址与 presence**（门牌号），不是授权。
// 真正的节点面鉴权是节点↔后端的 DH+HMAC（C-2），发生在 relay 到后端之后。
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/mynet-club/mynet_agent/mynetd/commands"
)

type Config struct {
	// Gateways 有序：公网入口在前，overlay 在后
	Gateways []string
	NodeID   string
	// HandshakeTimeout / IdleTimeout / Backoff*
	HandshakeTimeout time.Duration
	// IdleTimeout 允许「没有下行帧」的最长时长。超过即判定连接已死，主动断开重连。
	IdleTimeout time.Duration
	// PingInterval 发一次 ping（并顺带做一次 IdleTimeout 判活）的间隔。
	// 必须显著小于 IdleTimeout，否则判活要等到下一个 tick 才发生。
	PingInterval time.Duration
	BackoffBase  time.Duration
	BackoffMax   time.Duration
}

func DefaultConfig() Config {
	return Config{
		HandshakeTimeout: 10 * time.Second,
		IdleTimeout:      180 * time.Second,
		PingInterval:     30 * time.Second,
		BackoffBase:      2 * time.Second,
		BackoffMax:       2 * time.Minute,
	}
}

// Handlers 回调（命令分派 / 配置变更）
type Handlers struct {
	OnCommand func(ctx context.Context, req commands.Request) commands.Result
	OnEvent   func(name string, data map[string]string)
	// OnConnect 每次 WS 连接**建立**后被调用（首连和每次重连都算）。
	//
	// 为什么需要它：网关下发是 fire-and-forget 的事件帧，连接断开期间后端
	// 推过的 bundle_changed **不会补发**，通知丢了就永远丢。让「连上」这个动作
	// 自己补一次拉取（ETag 条件请求，无变化就是 304），断线窗口才不至于变成
	// 「配置永久滞后」。这是 WS 链路的自愈，不是心跳轮询。
	//
	// 跑在独立 goroutine 上，不得阻塞读循环；内部自行限时。
	OnConnect func(ctx context.Context)
	// OnResult 把命令结果编码成一帧上行数据（由调用方完成节点面签名）。
	// 返回 nil 表示不上报。
	//
	// 为什么是「编码成字节」而不是「直接签名」：transport 是**零密码学**的纯搬运层
	// （C-3），签名属节点面（C-2，node ↔ 后端），必须由组合根注入。
	// 此前 HandleFrame 拿到结果后 `_ = res` 直接丢弃，命令执行结果从不上报。
	OnResult func(ctx context.Context, res commands.Result) ([]byte, error)
}

// Client 到网关的长连接客户端。零密码学（C-3）。
type Client struct {
	cfg      Config
	handlers Handlers
	logf     func(string, ...any)

	mu     sync.Mutex
	stop   bool
	dialer Dialer
	// send 由 Dialer 在连接建立时注入、断开时清空。
	// 有了它，事件驱动的主动上报（如收到 command_queued 后拉取并执行命令、
	// 再回传结果）才可能——否则只有「被动应答下行帧」一条路。
	send func([]byte) error
}

// Send 主动发一帧到网关。未连接时返回明确错误。
//
// 与 HandleFrame 的返回值互补：后者是「应答下行帧」，Send 是「主动上行」。
func (c *Client) Send(frame []byte) error {
	c.mu.Lock()
	send := c.send
	c.mu.Unlock()
	if send == nil {
		return fmt.Errorf("transport: 未连接到网关，无法上行")
	}
	return send(frame)
}

// setSender 由 Dialer 调用：连接建立时注入，断开时传 nil 清空。
func (c *Client) setSender(f func([]byte) error) {
	c.mu.Lock()
	c.send = f
	c.mu.Unlock()
}

// fireOnConnect 由 Dialer 在连接建立后调用：跑一次「连上就该做的事」。
//
// 跑在独立 goroutine 上，绝不能在读循环之前同步等它 —— 补拉 bundle 走的是
// HTTP relay（另一条通路），慢或失败都不该拖住下行帧的接收。
// ctx 是这次连接的生命周期上下文：断开后补拉即使还在跑，也会被取消。
func (c *Client) fireOnConnect(ctx context.Context) {
	if c.handlers.OnConnect == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				c.logf("transport: OnConnect 回调 panic: %v", r)
			}
		}()
		c.handlers.OnConnect(ctx)
	}()
}

// Connected 报告当前是否有可用的上行通道。
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.send != nil
}

func New(cfg Config, h Handlers, logf func(string, ...any)) (*Client, error) {
	if len(cfg.Gateways) == 0 {
		return nil, fmt.Errorf("transport: Gateways 不能为空")
	}
	if cfg.NodeID == "" {
		return nil, fmt.Errorf("transport: NodeID 必填（寻址用）")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	// 始终过一遍 mergeDefaults：拨号实现现在会**用** IdleTimeout 判连接死活
	// （见 dial_websocket.go 的 pong 超时），漏配不能再退化成「恒为 0 → 一连就判死」。
	cfg = mergeDefaults(cfg)
	cl := &Client{cfg: cfg, handlers: h, logf: logf, dialer: WebSocketDialer}
	return cl, nil
}

func mergeDefaults(cfg Config) Config {
	d := DefaultConfig()
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = d.HandshakeTimeout
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = d.IdleTimeout
	}
	if cfg.PingInterval == 0 {
		cfg.PingInterval = d.PingInterval
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = d.BackoffBase
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = d.BackoffMax
	}
	return cfg
}

// ConnectHeaders 返回握手头：只带节点标识，不带签名（gateway 不鉴权）。
func (c *Client) ConnectHeaders() http.Header {
	h := http.Header{}
	h.Set("X-Node-Id", c.cfg.NodeID)
	return h
}

// Run 阻塞重连循环，直到 ctx 取消。
func (c *Client) Run(ctx context.Context) error {
	backoff := c.cfg.BackoffBase
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.mu.Lock()
		if c.stop {
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()

		err := c.dialOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.logf("transport: 连接结束: %v，%s 后重试", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter(backoff)):
		}
		backoff *= 2
		if backoff > c.cfg.BackoffMax {
			backoff = c.cfg.BackoffMax
		}
	}
}

func (c *Client) Stop() {
	c.mu.Lock()
	c.stop = true
	c.mu.Unlock()
}

// dialOnce 一次拨号。真实 gorilla/websocket 接入在依赖就绪后替换；
// 当前用可注入的 Dialer 便于测试。
func (c *Client) dialOnce(ctx context.Context) error {
	if c.dialer == nil {
		// 占位：无 Dialer 时仅保持重连循环可测
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
			return fmt.Errorf("dialer 未注入")
		}
	}
	return c.dialer(ctx, c)
}

// Dialer 测试/平台可注入
type Dialer func(ctx context.Context, c *Client) error

// SetDialer 注入拨号实现
func (c *Client) SetDialer(d Dialer) {
	c.mu.Lock()
	c.dialer = d
	c.mu.Unlock()
}

// HandleFrame 处理一帧网关下发的数据（由 Dialer 调用）。
//
// 返回需要回写给网关的帧（命令执行结果）；返回 nil 表示该帧无需回应。
// 本层不做任何签名——编码由 Handlers.OnResult 注入（见其注释）。
//
// ⚠️ 网关下发的是**事件信封** `{event, data}`，不是命令本身。后端的
// GatewayNotifier 只发三类事件（command_queued / bundle_changed / route_changed），
// 且 `data` 仅带定位信息（如 command_id）；真正的命令要节点自己回后端拉
// （`GET /api/v1/node/commands`）。
//
// 此前这里把**每一帧**都当 commands.Request 解析，于是 id/op 全空、
// 执行结果恒为 `unknown op: `、回报路径变成 `/api/v1/node/commands//result`
// ——命令链从未真通过。现在按信封优先分派，并保留裸命令帧的兼容路径。
func (c *Client) HandleFrame(ctx context.Context, raw []byte) ([]byte, error) {
	var envelope struct {
		Event string            `json:"event"`
		Data  map[string]string `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}

	if envelope.Event != "" {
		if c.handlers.OnEvent != nil {
			c.handlers.OnEvent(envelope.Event, envelope.Data)
		}
		return nil, nil
	}

	var req commands.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if req.Op == "" {
		// 既不是事件信封也不是命令帧：无从执行，也无从回报（没有 id 可关联）。
		return nil, fmt.Errorf("transport: 无法识别的帧（既无 event 也无 op）: %s", truncate(raw, 120))
	}
	if c.handlers.OnCommand == nil {
		return nil, nil
	}
	res := c.handlers.OnCommand(ctx, req)
	if c.handlers.OnResult == nil {
		return nil, nil
	}
	reply, err := c.handlers.OnResult(ctx, res)
	if err != nil {
		return nil, fmt.Errorf("transport: 编码命令结果失败: %w", err)
	}
	return reply, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

func jitter(d time.Duration) time.Duration {
	return d
}
