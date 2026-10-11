package mynet

import (
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func nftSnap() *shared.SystemSnapshot {
	return &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true}}
}

func argvOf(cmds []routeCmd) string {
	var b strings.Builder
	for _, c := range cmds {
		b.WriteString(strings.Join(c.argv, " "))
		b.WriteString("\n")
	}
	return b.String()
}

func TestProxyWGPathDisabledWithoutIfaceOrTargets(t *testing.T) {
	if got := proxyWGPathCmds(nftSnap(), ProxyRuntime{WGIface: "wg_exit"}); got != nil {
		t.Fatalf("无目的集不应出 wg 路径: %v", got)
	}
	if got := proxyWGPathCmds(nftSnap(), ProxyRuntime{WGTargets: []string{"1.1.1.0/24"}}); got != nil {
		t.Fatalf("无接口不应出 wg 路径: %v", got)
	}
}

func TestProxyWGPathEmitsSetRouteRuleMark(t *testing.T) {
	rt := ProxyRuntime{WGIface: "wg_exit", WGTargets: []string{"1.1.1.0/24", "2.2.2.0/24"}}
	out := argvOf(proxyWGPathCmds(nftSnap(), rt))

	for _, want := range []string{
		"nft add table ip mynet_proxy_wg",
		"nft add set ip mynet_proxy_wg mynet_proxy_wg",
		"nft add chain ip mynet_proxy_wg PREROUTING { type filter hook prerouting priority -160; }",
		"nft add element ip mynet_proxy_wg mynet_proxy_wg { 1.1.1.0/24 }",
		"nft add element ip mynet_proxy_wg mynet_proxy_wg { 2.2.2.0/24 }",
		"nft add rule ip mynet_proxy_wg PREROUTING ip daddr @mynet_proxy_wg meta mark set 0xc9 counter",
		"ip route replace default dev wg_exit table 201",
		"ip rule add fwmark 0xc9 lookup 201 prio 31799",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺 %q：\n%s", want, out)
		}
	}
}

func TestProxyMarkGuardedWhenWGEnabled(t *testing.T) {
	p := &domain.ProxyPolicy{Enabled: true, Role: domain.ProxyRoleClient, Region: domain.ProxyRegionDomestic, Policy: domain.ProxyPolicyWhitelist,
		Split: &domain.ProxySplit{ListSource: domain.ProxyListInternational, MatchMode: domain.ProxyMatchNormal}}

	// 无 wg：gnb 打标不应带 mark 0 守卫
	base := argvOf(proxyMarkCmds(nftSnap(), p, ProxyRuntime{TunDevice: "gnb_tun"}, p.EffectiveFwmark()))
	if strings.Contains(base, "meta mark 0 meta mark set") {
		t.Fatalf("未启用 wg 时不应加守卫：\n%s", base)
	}
	// 启用 wg：gnb 打标要加 `meta mark 0`，避免覆盖 wg 已打的 0xc9
	guarded := argvOf(proxyMarkCmds(nftSnap(), p,
		ProxyRuntime{TunDevice: "gnb_tun", WGIface: "wg_exit", WGTargets: []string{"1.1.1.0/24"}},
		p.EffectiveFwmark()))
	if !strings.Contains(guarded, "ip daddr @mynet_proxy meta mark 0 meta mark set 0xc8 counter") {
		t.Fatalf("启用 wg 时 gnb 打标应带 mark 0 守卫：\n%s", guarded)
	}
}

func TestWireGuardTargetsFromGroups(t *testing.T) {
	cfg := &domain.PluginProxyConfig{
		Exits: []domain.ProxyExit{{NodeID: "1001", Enabled: true, Driver: domain.ProxyExitDriverGnb}},
		Groups: []domain.ProxyGroup{
			{Name: "wg-grp", Targets: []string{"1.1.1.0/24", "2.2.2.0/24"},
				Exits: []domain.ProxyExit{{NodeID: "2002", Enabled: true, Driver: domain.ProxyExitDriverWireguard}}},
			{Name: "gnb-grp", Targets: []string{"3.3.3.0/24"},
				Exits: []domain.ProxyExit{{NodeID: "1001", Enabled: true}}},
		},
	}
	if !cfg.HasWireGuardExit() {
		t.Fatal("应识别到 wg 出口")
	}
	got, all := cfg.WireGuardTargets()
	if all {
		t.Fatal("非顶层 wg 出口不应 allViaWG")
	}
	if strings.Join(got, ",") != "1.1.1.0/24,2.2.2.0/24" {
		t.Fatalf("wg 目的集应为分组 targets：%v", got)
	}
}

func TestWireGuardTargetsTopLevelExitMeansAll(t *testing.T) {
	cfg := &domain.PluginProxyConfig{
		Exits: []domain.ProxyExit{{NodeID: "2002", Enabled: true, Driver: domain.ProxyExitDriverWireguard}},
	}
	targets, all := cfg.WireGuardTargets()
	if !all || targets != nil {
		t.Fatalf("顶层 wg 出口应 allViaWG=true：%v %v", targets, all)
	}
}
