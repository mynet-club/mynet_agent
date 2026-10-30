package main

import (
	"context"
	"testing"
	"time"
)

func TestRetryNodeFaceSucceedsAfterDelays(t *testing.T) {
	calls := 0
	done := make(chan struct{})
	go func() {
		retryNodeFace(context.Background(), 5*time.Millisecond, func() bool {
			calls++
			return calls >= 3
		}, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retry 未在期限内成功返回")
	}
	if calls != 3 {
		t.Fatalf("attempt 调用 %d 次, want 3（前两次失败第三次成功）", calls)
	}
}

func TestRetryNodeFaceStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		retryNodeFace(ctx, 5*time.Millisecond, func() bool { return false }, nil)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 retry 应立即返回")
	}
}
