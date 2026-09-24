package transport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent-V2/mynetd/commands"
)

// R1-5：帧 → Plan → Executor → Result（不经真 WS）
func TestCommandLoopGnbWgAndCross(t *testing.T) {
	ex := func(argv []string) (string, string, error) {
		return "ok:" + strings.Join(argv, " "), "", nil
	}
	cases := []struct {
		op      string
		wantSub string
	}{
		{commands.OpRefreshRoutesGnb, "refresh-routes"},
		{commands.OpRefreshAddressWG, "wireguard"},
		{commands.OpDetector, "detector"},
		{commands.OpMetrics, "metrics"},
	}
	for _, c := range cases {
		res := commands.Run(ex, commands.Request{ID: "1", Op: c.op, Args: map[string]string{"node_id": "9"}}, "9")
		if !res.OK {
			t.Fatalf("%s failed: %s", c.op, res.Error)
		}
		if !strings.Contains(res.Stdout, c.wantSub) {
			t.Fatalf("%s stdout=%q want %q", c.op, res.Stdout, c.wantSub)
		}
	}
}

func TestHandleFrameThenRun(t *testing.T) {
	var got commands.Result
	c, _ := New(Config{Gateways: []string{"ws://x"}, NodeID: "n"}, Handlers{
		OnCommand: func(_ context.Context, req commands.Request) commands.Result {
			got = commands.Run(func(argv []string) (string, string, error) {
				return strings.Join(argv, " "), "", nil
			}, req, "n")
			return got
		},
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.HandleFrame(ctx, []byte(`{"id":"c1","op":"gnb.refresh.route","args":{"node_id":"1"}}`)); err != nil {
		t.Fatal(err)
	}
	if !got.OK || !strings.Contains(got.Stdout, "refresh-routes") {
		t.Fatalf("got=%+v", got)
	}
}
