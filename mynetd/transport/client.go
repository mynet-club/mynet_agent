// Package transport — mynetd → gateway 的 WS 长连接（S3-1）。
//
// 设计（C-3/C-4）：gateway **不鉴权**，只做透传 + 连接状态表。
// 连接时带 X-Node-Id 仅作**寻址与 presence**（门牌号），不是授权。
// 真正的节点面鉴权是节点↔back 的 DH+HMAC（C-2），发生在 relay 到 back 之后。
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/mynet-club/mynet_agent-V2/mynetd/commands"
)

type Config struct {
	// Gateways 有序：公网入口在前，overlay 在后
	Gateways []string
	NodeID   string
	// HandshakeTimeout / IdleTimeout / Backoff*
	HandshakeTimeout time.Duration
	IdleTimeout      time.Duration
	BackoffBase      time.Duration
	BackoffMax       time.Duration
}

func DefaultConfig() Config {
	return Config{
		HandshakeTimeout: 10 * time.Second,
		IdleTimeout:      180 * time.Second,
		BackoffBase:      2 * time.Second,
		BackoffMax:       2 * time.Minute,
	}
}

// Handlers 回调（命令分派 / 配置变更）
type Handlers struct {
	OnCommand func(ctx context.Context, req commands.Request) commands.Result
	OnEvent   func(name string, data map[string]string)
}

// Client 到网关的长连接客户端。零密码学（C-3）。
type Client struct {
	cfg      Config
	handlers Handlers
	logf     func(string, ...any)

	mu     sync.Mutex
	stop   bool
	dialer Dialer
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
	if cfg.HandshakeTimeout == 0 {
		cfg = mergeDefaults(cfg)
	}
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

// HandleFrame 处理一帧 JSON 命令（由 Dialer 调用）
func (c *Client) HandleFrame(ctx context.Context, raw []byte) error {
	var req commands.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return err
	}
	if c.handlers.OnCommand == nil {
		return nil
	}
	res := c.handlers.OnCommand(ctx, req)
	_ = res
	return nil
}

func jitter(d time.Duration) time.Duration {
	return d
}
