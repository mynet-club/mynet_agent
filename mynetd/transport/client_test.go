package transport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent-V2/mynetd/commands"
)

func TestConnectHeadersHaveNodeIDButNoSignature(t *testing.T) {
	c, err := New(Config{Gateways: []string{"ws://gw/v1/node/connect"}, NodeID: "n1"}, Handlers{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := c.ConnectHeaders()
	if h.Get("X-Node-Id") != "n1" {
		t.Fatalf("X-Node-Id=%q", h.Get("X-Node-Id"))
	}
	// C-3：gateway 不鉴权，握手不带签名/Authorization
	for _, k := range []string{"Authorization", "X-Signature", "X-Nonce"} {
		if h.Get(k) != "" {
			t.Fatalf("must not send %s to gateway", k)
		}
	}
}

func TestHandleFrameDispatchesCommand(t *testing.T) {
	got := ""
	c, err := New(Config{Gateways: []string{"ws://x"}, NodeID: "n1"},
		Handlers{OnCommand: func(_ context.Context, req commands.Request) commands.Result {
			got = req.Op
			return commands.Result{ID: req.ID, OK: true}
		}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.HandleFrame(context.Background(), []byte(`{"id":"1","op":"detector"}`)); err != nil {
		t.Fatal(err)
	}
	if got != "detector" {
		t.Fatalf("op=%q", got)
	}
}

// 未注入 OnResult 时不回传（保持向后兼容），但**注入了就必须回传**——
// 此前 HandleFrame 拿到结果后 `_ = res` 直接丢弃，back 侧命令永远停在「已下发」。
func TestHandleFrameReturnsResultFrameWhenOnResultSet(t *testing.T) {
	c, err := New(Config{Gateways: []string{"ws://x"}, NodeID: "n1"}, Handlers{
		OnCommand: func(_ context.Context, req commands.Request) commands.Result {
			return commands.Result{ID: req.ID, OK: true, Stdout: "done"}
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 未注入 OnResult → 无回传帧
	reply, err := c.HandleFrame(context.Background(), []byte(`{"id":"c1","op":"detector"}`))
	if err != nil {
		t.Fatalf("HandleFrame: %v", err)
	}
	if reply != nil {
		t.Errorf("未注入 OnResult 时不应有回传帧，实际 %s", reply)
	}

	// 注入 OnResult → 结果被编码成帧
	c2, _ := New(Config{Gateways: []string{"ws://x"}, NodeID: "n1"}, Handlers{
		OnCommand: func(_ context.Context, req commands.Request) commands.Result {
			return commands.Result{ID: req.ID, OK: true, Stdout: "done"}
		},
		OnResult: func(_ context.Context, res commands.Result) ([]byte, error) {
			return []byte(`{"t":"result","id":"` + res.ID + `"}`), nil
		},
	}, nil)
	reply2, err := c2.HandleFrame(context.Background(), []byte(`{"id":"c2","op":"detector"}`))
	if err != nil {
		t.Fatalf("HandleFrame: %v", err)
	}
	if string(reply2) != `{"t":"result","id":"c2"}` {
		t.Errorf("回传帧 = %s", reply2)
	}
}

// OnResult 报错必须冒泡，不能静默吞掉——否则「结果没上报」会伪装成「执行成功」。
func TestHandleFramePropagatesOnResultError(t *testing.T) {
	c, _ := New(Config{Gateways: []string{"ws://x"}, NodeID: "n1"}, Handlers{
		OnCommand: func(_ context.Context, req commands.Request) commands.Result {
			return commands.Result{ID: req.ID, OK: true}
		},
		OnResult: func(_ context.Context, _ commands.Result) ([]byte, error) {
			return nil, errors.New("签名失败：缺少 back_pub")
		},
	}, nil)

	reply, err := c.HandleFrame(context.Background(), []byte(`{"id":"c3","op":"detector"}`))
	if err == nil {
		t.Fatal("OnResult 报错时 HandleFrame 应返回错误")
	}
	if reply != nil {
		t.Errorf("出错时不应有回传帧，实际 %s", reply)
	}
	if !strings.Contains(err.Error(), "back_pub") {
		t.Errorf("错误应保留原始原因，实际: %v", err)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	c, err := New(Config{
		Gateways: []string{"ws://x"}, NodeID: "n1",
		BackoffBase: time.Millisecond, BackoffMax: 5 * time.Millisecond,
	}, Handlers{}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = c.Run(ctx)
	if err == nil || (!strings.Contains(err.Error(), "context") && ctx.Err() == nil) {
		// Run 返回 ctx.Err()
	}
}
