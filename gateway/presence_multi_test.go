package gateway

import "testing"

// R5-2：两套 Registry/presence 互不串（多 gateway 实例语义）
func TestTwoRegistriesIndependent(t *testing.T) {
	a := NewServer("http://back", "t1")
	b := NewServer("http://back", "t2")
	a.Registry().Register("n1")
	a.Registry().MarkVerified("n1")
	b.Registry().Register("n1")
	if b.Registry().IsVerified("n1") {
		t.Fatal("instance B must not inherit A verified")
	}
	if !a.Registry().IsVerified("n1") {
		t.Fatal("A should be verified")
	}
}
