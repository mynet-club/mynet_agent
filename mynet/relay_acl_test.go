package mynet

import (
	"reflect"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

func relayTestNodes() []domain.GnbNode {
	return []domain.GnbNode{
		{NodeID: "a", TunAddress: "10.182.236.181/32"},
		{NodeID: "b", TunAddress: "10.182.236.185/32"},
	}
}

func bothCfg(exits ...string) *domain.PluginProxyConfig {
	c := &domain.PluginProxyConfig{Role: "both"}
	for _, e := range exits {
		c.Exits = append(c.Exits, domain.ProxyExit{NodeID: e, Enabled: true})
	}
	return c
}

func TestDeriveRelayACLDisabled(t *testing.T) {
	cases := map[string]*domain.PluginProxyConfig{
		"nil":         nil,
		"client":      {Role: "client", Exits: []domain.ProxyExit{{NodeID: "x", Enabled: true}}},
		"server":      {Role: "server"},
		"both-noexit": bothCfg(),
	}
	for name, pp := range cases {
		if got := deriveRelayACL(pp, nil, relayTestNodes()); got.Enabled {
			t.Fatalf("%s: want disabled, got %+v", name, got)
		}
	}
}

func TestDeriveRelayACLDefaultOpen(t *testing.T) {
	got := deriveRelayACL(bothCfg("4997342800601732"), nil, relayTestNodes())
	if !got.Enabled || got.Mode != domain.ProxyACLOpen || len(got.Allow) != 0 || len(got.Deny) != 0 {
		t.Fatalf("want enabled open, got %+v", got)
	}
	perm := &domain.ProxyPermission{Enabled: true} // ACL 空 = open
	got = deriveRelayACL(bothCfg("4997342800601732"), perm, relayTestNodes())
	if !got.Enabled || got.Mode != domain.ProxyACLOpen {
		t.Fatalf("perm without ACL must stay open, got %+v", got)
	}
}

func TestDeriveRelayACLAllowlist(t *testing.T) {
	perm := &domain.ProxyPermission{Enabled: true, ACL: &domain.ProxyACL{
		Mode:           domain.ProxyACLAllowlist,
		AllowedNodeIDs: []string{"a", "a", "unknown"},
		AllowedCIDRs:   []string{"10.20.0.0/24", "not-a-cidr", "10.20.0.0/24"},
		DeniedNodeIDs:  []string{"b"},
	}}
	got := deriveRelayACL(bothCfg("4997342800601732"), perm, relayTestNodes())
	if !got.Enabled || got.Mode != domain.ProxyACLAllowlist {
		t.Fatalf("mode: %+v", got)
	}
	wantAllow := []string{"10.182.236.181/32", "10.20.0.0/24"}
	if !reflect.DeepEqual(got.Allow, wantAllow) {
		t.Fatalf("allow=%v want %v", got.Allow, wantAllow)
	}
	wantDeny := []string{"10.182.236.185/32"}
	if !reflect.DeepEqual(got.Deny, wantDeny) {
		t.Fatalf("deny=%v want %v", got.Deny, wantDeny)
	}
}

func TestDeriveRelayACLDenylist(t *testing.T) {
	perm := &domain.ProxyPermission{Enabled: true, ACL: &domain.ProxyACL{
		Mode:          domain.ProxyACLDenylist,
		DeniedNodeIDs: []string{"b"},
	}}
	got := deriveRelayACL(bothCfg("4997342800601732"), perm, relayTestNodes())
	if !got.Enabled || got.Mode != domain.ProxyACLDenylist || len(got.Allow) != 0 {
		t.Fatalf("mode/allow: %+v", got)
	}
	if !reflect.DeepEqual(got.Deny, []string{"10.182.236.185/32"}) {
		t.Fatalf("deny=%v", got.Deny)
	}
}
