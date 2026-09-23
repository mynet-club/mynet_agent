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
	_ = p.Publish(ctx, "n1")
	select {
	case <-ch:
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
