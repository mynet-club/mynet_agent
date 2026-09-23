package localapi

import (
	"fmt"
	"testing"
	"time"
)

// R5-5 本地微基准：1k 会话 attach/focus（无网络）
func BenchmarkSessionTable1k(b *testing.B) {
	tb := NewSessionTable()
	for i := 0; i < 1000; i++ {
		tb.Attach(fmt.Sprintf("n%d", i), "gnb")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tb.Focus(fmt.Sprintf("n%d", i%1000))
		_ = tb.ParallelCount()
	}
}

func TestSessionTable1kSmoke(t *testing.T) {
	tb := NewSessionTable()
	start := time.Now()
	for i := 0; i < 1000; i++ {
		tb.Attach(fmt.Sprintf("n%d", i), "dual")
	}
	for i := 0; i < 1000; i++ {
		tb.Focus(fmt.Sprintf("n%d", i))
	}
	d := time.Since(start)
	if tb.ParallelCount() != 1000 {
		t.Fatalf("count=%d", tb.ParallelCount())
	}
	if d > 2*time.Second {
		t.Fatalf("too slow: %s", d)
	}
	t.Logf("1k attach+focus in %s", d)
}
