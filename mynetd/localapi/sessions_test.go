package localapi

import "testing"

// R5-1：并行会话 + 焦点切换不互踢
func TestParallelSessionsAndFocus(t *testing.T) {
	tb := NewSessionTable()
	tb.Attach("n1", "gnb")
	tb.Attach("n2", "wireguard")
	tb.Attach("n3", "dual")
	if tb.ParallelCount() != 3 {
		t.Fatalf("parallel=%d", tb.ParallelCount())
	}
	if tb.Focused() != "n1" {
		t.Fatalf("focus=%s want n1 (first)", tb.Focused())
	}
	if !tb.Focus("n3") {
		t.Fatal("focus n3")
	}
	if tb.Focused() != "n3" || tb.ParallelCount() != 3 {
		t.Fatalf("switch must not detach others: focus=%s n=%d", tb.Focused(), tb.ParallelCount())
	}
	list := tb.List()
	if len(list) != 3 || !list[2].Focused {
		t.Fatalf("list=%+v", list)
	}
	tb.Detach("n3")
	if tb.Focused() == "n3" {
		t.Fatal("detached must not stay focus")
	}
	if tb.ParallelCount() != 2 {
		t.Fatal("count")
	}
}
