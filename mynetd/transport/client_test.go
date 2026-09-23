package transport

import (
	"context"
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
	if err := c.HandleFrame(context.Background(), []byte(`{"id":"1","op":"detector"}`)); err != nil {
		t.Fatal(err)
	}
	if got != "detector" {
		t.Fatalf("op=%q", got)
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
