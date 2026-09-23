package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// WebSocketDialer 真 WS 拨号（C-3：只带 X-Node-Id，无签名）。
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

	// 读循环：命令帧 → HandleFrame
	go func() {
		defer cancel()
		for {
			_, raw, err := conn.Read(readCtx)
			if err != nil {
				c.logf("transport: read end: %v", err)
				return
			}
			if err := c.HandleFrame(readCtx, raw); err != nil {
				c.logf("transport: handle frame: %v", err)
			}
		}
	}()

	// 心跳帧（可选；back 也走 HTTP 心跳）
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-readCtx.Done():
			return fmt.Errorf("read loop ended")
		case <-tick.C:
			ping, _ := json.Marshal(map[string]string{"t": "ping", "node_id": c.cfg.NodeID})
			if err := conn.Write(ctx, websocket.MessageText, ping); err != nil {
				return err
			}
		}
	}
}

// AutoDialer 平台默认：WS 优先
func AutoDialer() Dialer { return WebSocketDialer }
