package mynet

import "testing"

func TestRenderGuestRouteConfSelfFirst(t *testing.T) {
	nodes := []guestNode{
		{NodeID: 1002, VirtualIP: "10.1.0.2"},
		{NodeID: 1001, VirtualIP: "10.1.0.1"},
		{NodeID: 1003, VirtualIP: "10.1.0.3"},
	}
	got := renderGuestRouteConf(1001, nodes)
	want := "1001|10.1.0.1|255.255.255.255\n1002|10.1.0.2|255.255.255.255\n1003|10.1.0.3|255.255.255.255\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderGuestRouteConfMissingSelf(t *testing.T) {
	nodes := []guestNode{{NodeID: 2, VirtualIP: "10.1.0.2"}}
	if renderGuestRouteConf(1, nodes) != "" {
		t.Fatal("self 不在表内应返回空")
	}
}
