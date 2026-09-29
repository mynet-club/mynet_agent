package domain

import "testing"

func TestProxyACLModes(t *testing.T) {
	open := &ProxyACL{}
	if !open.IsAllowed("any") {
		t.Fatal("open 应全放行")
	}
	al := &ProxyACL{
		Mode:           ProxyACLAllowlist,
		AllowedNodeIDs: []string{"1001", "1002"},
		DeniedNodeIDs:  []string{"1002"},
	}
	if !al.IsAllowed("1001") {
		t.Fatal("1001 应放行")
	}
	if al.IsAllowed("1002") {
		t.Fatal("deny 优先于 allow")
	}
	if al.IsAllowed("9999") {
		t.Fatal("allowlist 外应拒")
	}
	dl := &ProxyACL{
		Mode:          ProxyACLDenylist,
		DeniedNodeIDs: []string{"bad"},
	}
	if dl.IsAllowed("good") {
		// good 不在 deny
	} else {
		t.Fatal("denylist 外应放行")
	}
	if dl.IsAllowed("bad") {
		t.Fatal("denylist 应拒")
	}
}

func TestProxyACLValidate(t *testing.T) {
	bad := &ProxyACL{Mode: ProxyACLAllowlist}
	if err := bad.Validate(); err == nil {
		t.Fatal("allowlist 空名单应拒")
	}
	ok := &ProxyACL{Mode: ProxyACLAllowlist, AllowedNodeIDs: []string{"1"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestProxyACLFilterAllowed(t *testing.T) {
	a := &ProxyACL{Mode: ProxyACLAllowlist, AllowedNodeIDs: []string{"2", "4"}}
	got := a.FilterAllowed([]string{"1", "2", "3", "4"})
	if len(got) != 2 || got[0] != "2" || got[1] != "4" {
		t.Fatalf("got=%v", got)
	}
}
