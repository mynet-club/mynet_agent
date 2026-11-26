package mynet

import (
	"strings"
	"testing"
)

func TestFirewallIncludeIPTablesGatewayRules(t *testing.T) {
	body, err := firewallIncludeScriptIPTables("gnb_tun", "br-lan")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "nft") {
		t.Fatalf("fw3 include must not reference nft:\n%s", body)
	}
	for _, want := range []string{
		`DEV="gnb_tun"`,
		`WAN="br-lan"`,
		`-o "$DEV" -j MASQUERADE`,
		`-i "$DEV" -j MARK --set-xmark "$MARK"`,
		`-m mark --mark "$MARK" -o "$WAN" -j MASQUERADE -m comment --comment "` + GatewayForwardComment + `"`,
		`-I FORWARD 1 -i "$DEV" -j "$CHAIN_M"`,
		`-I POSTROUTING 1 -j "$CHAIN_N"`,
		// 幂等/清理：每次先删跳转→清链→删链，再重建
		`del_jumps nat POSTROUTING "$CHAIN_N"`,
		`iptables -t nat -F "$CHAIN_N"`,
		`iptables -t nat -X "$CHAIN_N"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in include:\n%s", want, body)
		}
	}
}

func TestFirewallIncludeIPTablesNoWAN(t *testing.T) {
	body, err := firewallIncludeScriptIPTables("gnb_tun", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, GatewayForwardComment) {
		t.Fatalf("non-gateway include must not carry gateway comment:\n%s", body)
	}
	if !strings.Contains(body, `-m mark --mark "$MARK" -j MASQUERADE`) {
		t.Fatalf("missing generic mark masquerade:\n%s", body)
	}
}

func TestFirewallIncludeIPTablesEmptyTunCleansUp(t *testing.T) {
	body, err := firewallIncludeScriptIPTables("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `del_jumps mangle FORWARD "$CHAIN_M"`) ||
		!strings.Contains(body, `del_jumps nat POSTROUTING "$CHAIN_N"`) ||
		!strings.Contains(body, "exit 0") {
		t.Fatalf("empty tun must render cleanup branch:\n%s", body)
	}
	if strings.Contains(body, `iptables -t nat -N "$CHAIN_N"`) {
		t.Fatalf("empty tun must not create chains:\n%s", body)
	}
}

func TestFirewallIncludeIPTablesRejectsBadDev(t *testing.T) {
	if _, err := firewallIncludeScriptIPTables("bad;flush", ""); err == nil {
		t.Fatal("invalid interface name accepted")
	}
}
