package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/mynet/driver"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func wgEnvForProjection() (*domain.WireGuardIntent, *domain.ProxyPolicy) {
	w := &domain.WireGuardIntent{
		Interface: domain.WgInterface{PrivateKey: "priv", PublicKey: "pub", KeyAlgorithm: "curve25519"},
		Peers: []domain.WgPeer{
			{ID: "p1", BusinessNodeID: "1001", PublicKey: "peerkey", AllowedIPs: []string{"10.0.0.0/24"}},
			{ID: "p2", BusinessNodeID: "2002", PublicKey: "other", AllowedIPs: []string{"10.9.0.0/24"}},
		},
	}
	p := &domain.ProxyPolicy{
		Enabled: true,
		Role:    domain.ProxyRoleClient,
		Region:  domain.ProxyRegionDomestic,
		Policy:  domain.ProxyPolicyWhitelist,
		Exits:   []domain.ProxyExit{{NodeID: "1001", Enabled: true, Weight: 10}},
	}
	return w, p
}

func TestProjectProxyToWGUnionsExitAllowedIPs(t *testing.T) {
	w, p := wgEnvForProjection()
	dest := []string{"1.1.1.0/24", "2.2.2.0/24"}
	if err := ProjectProxyToWG(w, p, dest); err != nil {
		t.Fatalf("投影失败: %v", err)
	}
	// 出口 peer 原有网段保留，目的集并入
	got := strings.Join(w.Peers[0].AllowedIPs, ",")
	for _, want := range []string{"10.0.0.0/24", "1.1.1.0/24", "2.2.2.0/24"} {
		if !strings.Contains(got, want) {
			t.Fatalf("出口 peer 缺失 %s：%s", want, got)
		}
	}
	// 非出口 peer 不被污染
	if strings.Join(w.Peers[1].AllowedIPs, ",") != "10.9.0.0/24" {
		t.Fatalf("非出口 peer 被改写: %v", w.Peers[1].AllowedIPs)
	}
	// 接管路由
	if w.Routing == nil || w.Routing.Mode != "off" {
		t.Fatalf("应设 Table=off 接管路由，实际 %+v", w.Routing)
	}
}

func TestProjectProxyToWGIdempotent(t *testing.T) {
	w, p := wgEnvForProjection()
	dest := []string{"1.1.1.0/24", "2.2.2.0/24"}
	if err := ProjectProxyToWG(w, p, dest); err != nil {
		t.Fatal(err)
	}
	first := strings.Join(w.Peers[0].AllowedIPs, ",")
	if err := ProjectProxyToWG(w, p, dest); err != nil {
		t.Fatal(err)
	}
	if second := strings.Join(w.Peers[0].AllowedIPs, ","); second != first {
		t.Fatalf("非幂等：%s → %s", first, second)
	}
}

func TestProjectProxyToWGFailLoudMissingExit(t *testing.T) {
	w, p := wgEnvForProjection()
	p.Exits = []domain.ProxyExit{{NodeID: "9999", Enabled: true}} // 选了不存在的出口
	err := ProjectProxyToWG(w, p, []string{"1.1.1.0/24"})
	if err == nil {
		t.Fatal("出口对不上任何 peer 时应 fail-loud，实际通过")
	}
	if !strings.Contains(err.Error(), "不被任何 peer 的 AllowedIPs 覆盖") {
		t.Fatalf("错误信息应说明覆盖缺失: %v", err)
	}
}

func TestProjectProxyToWGNoopWhenNotClient(t *testing.T) {
	w, p := wgEnvForProjection()
	p.Enabled = false
	if err := ProjectProxyToWG(w, p, []string{"1.1.1.0/24"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(w.Peers[0].AllowedIPs, ",") != "10.0.0.0/24" || w.Routing != nil {
		t.Fatalf("禁用时不应改动: allowed=%v routing=%+v", w.Peers[0].AllowedIPs, w.Routing)
	}

	w2, p2 := wgEnvForProjection()
	p2.Role = domain.ProxyRoleServer
	if err := ProjectProxyToWG(w2, p2, []string{"1.1.1.0/24"}); err != nil {
		t.Fatal(err)
	}
	if w2.Routing != nil {
		t.Fatalf("server 角色不应接管路由: %+v", w2.Routing)
	}
}

func TestWGProxyDestCIDRsGlobalModes(t *testing.T) {
	// FullTunnel → /0
	if got, err := WGProxyDestCIDRs(&domain.ProxyPolicy{Enabled: true, FullTunnel: true}); err != nil || len(got) != 1 || got[0] != "0.0.0.0/0" {
		t.Fatalf("FullTunnel 应给 0.0.0.0/0: %v %v", got, err)
	}
	// inverted（未命中列表即代理）→ 安全超集 /0
	p := &domain.ProxyPolicy{Enabled: true, Split: &domain.ProxySplit{MatchMode: domain.ProxyMatchInverted, ListSource: domain.ProxyListChina}}
	if got, err := WGProxyDestCIDRs(p); err != nil || len(got) != 1 || got[0] != "0.0.0.0/0" {
		t.Fatalf("inverted 应给 0.0.0.0/0: %v %v", got, err)
	}
}

func TestWGProxyDestCIDRsFromBundledList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	dir := filepath.Join(home, "conf", "proxy", "proxy_sources")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "interip.txt"), []byte("# c\n1.1.1.0/24\n8.8.8.8/32\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// domestic + whitelist → international → interip.txt
	p := &domain.ProxyPolicy{
		Enabled: true, Region: domain.ProxyRegionDomestic, Policy: domain.ProxyPolicyWhitelist,
		Split: &domain.ProxySplit{ListSource: domain.ProxyListInternational, MatchMode: domain.ProxyMatchNormal},
	}
	got, err := WGProxyDestCIDRs(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "1.1.1.0/24" || got[1] != "8.8.8.8/32" {
		t.Fatalf("应从随包列表解出 2 条 CIDR: %v", got)
	}
}

func TestRenderWGConfigTableOff(t *testing.T) {
	w := &domain.WireGuardIntent{
		Interface: domain.WgInterface{PrivateKey: "priv", KeyAlgorithm: "curve25519"},
		Peers:     []domain.WgPeer{{PublicKey: "k", AllowedIPs: []string{"1.1.1.0/24"}}},
		Routing:   &domain.WgRouting{Mode: "off"},
	}
	if text := driver.RenderWGConfig(w); !strings.Contains(text, "Table = off") {
		t.Fatalf("应在 [Interface] 渲染 Table = off:\n%s", text)
	}
	w.Routing = nil
	if text := driver.RenderWGConfig(w); strings.Contains(text, "Table = off") {
		t.Fatalf("未接管路由时不应出现 Table = off:\n%s", text)
	}
}
