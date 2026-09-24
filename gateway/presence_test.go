package gateway

import (
	"context"
	"testing"
	"time"
)

func TestMemoryPresenceOnlineVerified(t *testing.T) {
	p := NewMemoryPresence()
	ctx := context.Background()
	_ = p.SetOnline(ctx, "n1", false)
	on, ver, _ := p.IsOnline(ctx, "n1")
	if !on || ver {
		t.Fatalf("on=%v ver=%v", on, ver)
	}
	// C-4 置位后 verified
	_ = p.SetOnline(ctx, "n1", true)
	_, ver, _ = p.IsOnline(ctx, "n1")
	if !ver {
		t.Fatal("should be verified")
	}
	_ = p.SetOffline(ctx, "n1")
	on, _, _ = p.IsOnline(ctx, "n1")
	if on {
		t.Fatal("should be offline")
	}
}

func TestMemoryPresencePubSub(t *testing.T) {
	p := NewMemoryPresence()
	ctx := context.Background()
	ch, err := p.Subscribe(ctx, "n1")
	if err != nil {
		t.Fatal(err)
	}
	// 发**帧原文**。这是接口改动的核心（V4-5）：跨实例要能还原 event/data，
	// 只发空 struct 的话，接收方收到信号也拼不出要推给节点的东西
	// —— 原 Publish 还把 payload 硬编码成 {"event":"queued"}，实属半成品。
	frame := []byte(`{"event":"command_queued","data":{"command_id":"9007199254740993"}}`)
	if err := p.Publish(ctx, "n1", frame); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ch:
		if string(got) != string(frame) {
			t.Fatalf("收到的帧与发布的不一致:\n  发布: %s\n  收到: %s", frame, got)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
}

func TestRedisEmptyAddrFallsBackToMemory(t *testing.T) {
	// C-6：Redis 挂/未配 → 退回单实例内存，行为明确
	s, err := NewRedisPresence("", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = s.SetOnline(ctx, "n1", true)
	on, ver, _ := s.IsOnline(ctx, "n1")
	if !on || !ver {
		t.Fatalf("fallback broken on=%v ver=%v", on, ver)
	}
}

func TestRedisUnreachableFallsBackToMemory(t *testing.T) {
	// C-6 故障降级：Ping 失败 → MemoryPresence
	s, err := NewRedisPresence("127.0.0.1:1", "x", 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = s.SetOnline(ctx, "n9", true)
	on, ver, _ := s.IsOnline(ctx, "n9")
	if !on || !ver {
		t.Fatalf("fallback broken on=%v ver=%v", on, ver)
	}
}
