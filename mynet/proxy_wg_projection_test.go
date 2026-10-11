package mynet

import (
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/mynet/driver"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func wgIntent(peerIDs ...string) *domain.WireGuardIntent {
	w := &domain.WireGuardIntent{Interface: domain.WgInterface{PrivateKey: "priv", KeyAlgorithm: "curve25519"}}
	for _, id := range peerIDs {
		w.Peers = append(w.Peers, domain.WgPeer{BusinessNodeID: id, PublicKey: "pk-" + id, AllowedIPs: []string{"10.0.0.0/24"}})
	}
	return w
}

// 映射必须与 gnb 的 proxy.conf 同义：
//
//	默认出口（[default]）→ 0.0.0.0/0（wg 无兜底出口）；分组出口 → 组内 targets（最长前缀）；
//	组内无出口 = DROP（不给任何 peer 放行）。
func TestWGExitDestinationsDefaultAndGroups(t *testing.T) {
	c := &domain.PluginProxyConfig{
		Exits: []domain.ProxyExit{{NodeID: "1001", Enabled: true}},
		Groups: []domain.ProxyGroup{
			{Name: "jp", Targets: []string{"1.1.1.0/24", "0.0.0.0/0"}, Exits: []domain.ProxyExit{{NodeID: "2002", Enabled: true}}},
			{Name: "drop", Targets: []string{"9.9.9.0/24"}}, // 无出口 → DROP
		},
	}
	got := WGExitDestinations(c)
	if len(got["1001"]) != 1 || got["1001"][0] != "0.0.0.0/0" {
		t.Fatalf("默认出口应放全（0.0.0.0/0）：%v", got["1001"])
	}
	if len(got["2002"]) != 1 || got["2002"][0] != "1.1.1.0/24" {
		t.Fatalf("分组出口只放组内 targets（/0 拒载）：%v", got["2002"])
	}
	if len(got) != 2 {
		t.Fatalf("应为 2 个出口条目（无出口的组不产生条目）：%v", got)
	}
}

func TestWGExitDestinationsDisabledExitIgnored(t *testing.T) {
	c := &domain.PluginProxyConfig{
		Exits:  []domain.ProxyExit{{NodeID: "1001", Enabled: false}},
		Groups: []domain.ProxyGroup{{Name: "g", Targets: []string{"1.1.1.0/24"}, Exits: []domain.ProxyExit{{NodeID: "2002", Enabled: false}}}},
	}
	if got := WGExitDestinations(c); len(got) != 0 {
		t.Fatalf("未启用的出口不该产生条目：%v", got)
	}
}

func TestProjectProxyToWGProjectsAndTableOff(t *testing.T) {
	w := wgIntent("1001", "2002")
	if err := ProjectProxyToWG(w, map[string][]string{"1001": {"0.0.0.0/0"}, "2002": {"1.1.1.0/24"}}); err != nil {
		t.Fatalf("投影失败: %v", err)
	}
	a0 := strings.Join(w.Peers[0].AllowedIPs, ",")
	if !strings.Contains(a0, "0.0.0.0/0") || !strings.Contains(a0, "10.0.0.0/24") {
		t.Fatalf("默认出口应并集 0.0.0.0/0 且保留原有网段：%s", a0)
	}
	a1 := strings.Join(w.Peers[1].AllowedIPs, ",")
	if !strings.Contains(a1, "1.1.1.0/24") || strings.Contains(a1, "0.0.0.0/0") {
		t.Fatalf("分组出口只该拿自己那份：%s", a1)
	}
	if w.Routing == nil || w.Routing.Mode != "off" {
		t.Fatalf("必须接管路由（Table=off），否则 0.0.0.0/0 会抢走默认路由：%+v", w.Routing)
	}
}

func TestProjectProxyToWGIdempotent(t *testing.T) {
	w := wgIntent("1001")
	dest := map[string][]string{"1001": {"0.0.0.0/0"}}
	if err := ProjectProxyToWG(w, dest); err != nil {
		t.Fatal(err)
	}
	first := strings.Join(w.Peers[0].AllowedIPs, ",")
	if err := ProjectProxyToWG(w, dest); err != nil {
		t.Fatal(err)
	}
	if second := strings.Join(w.Peers[0].AllowedIPs, ","); second != first {
		t.Fatalf("非幂等：%s → %s", first, second)
	}
}

// 出口选点在 wg bundle 里对不上 peer → fail-loud（否则包被 cryptokey routing 静默丢）。
func TestProjectProxyToWGFailLoudMissingExit(t *testing.T) {
	w := wgIntent("1001")
	err := ProjectProxyToWG(w, map[string][]string{"9999": {"1.1.1.0/24"}})
	if err == nil {
		t.Fatal("出口对不上任何 peer 时应 fail-loud")
	}
	if !strings.Contains(err.Error(), "对不上 peer") {
		t.Fatalf("错误信息应指出对不上：%v", err)
	}
}

func TestProjectProxyToWGNoopWhenNothingToProject(t *testing.T) {
	w := wgIntent("1001")
	before := strings.Join(w.Peers[0].AllowedIPs, ",")
	if err := ProjectProxyToWG(w, nil); err != nil {
		t.Fatal(err)
	}
	if after := strings.Join(w.Peers[0].AllowedIPs, ","); after != before || w.Routing != nil {
		t.Fatalf("没有出口时不该改动：%s routing=%+v", after, w.Routing)
	}
}

func TestRenderWGConfigTableOff(t *testing.T) {
	w := &domain.WireGuardIntent{
		Interface: domain.WgInterface{PrivateKey: "priv", KeyAlgorithm: "curve25519"},
		Peers:     []domain.WgPeer{{PublicKey: "k", AllowedIPs: []string{"1.1.1.0/24"}}},
		Routing:   &domain.WgRouting{Mode: "off"},
	}
	if text := driver.RenderWGConfig(w, nil); !strings.Contains(text, "Table = off") {
		t.Fatalf("应在 [Interface] 渲染 Table = off:\n%s", text)
	}
	w.Routing = nil
	if text := driver.RenderWGConfig(w, nil); strings.Contains(text, "Table = off") {
		t.Fatalf("未接管路由时不应出现 Table = off:\n%s", text)
	}
}
