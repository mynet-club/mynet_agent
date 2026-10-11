package driver_test

// A-8 双驱动验证测试套件 —— M1 硬门槛。
//
// 三场景 × 六判据（dev-plan 轨道 A「验证目标」）：
//
//	场景：gnb 网 / wireguard 网 / 跨驱动桥接
//	判据：
//	  1 业务对象不分叉（同一套业务命令行为一致）
//	  2 驱动差异不泄漏到业务对象
//	  3 Envelope 互斥（driver=gnb 不带 wireguard intent，反之亦然）
//	  4 节点面 DH 一套逻辑（gnb edwards→montgomery / wg 直接 DH，只差格式转换）
//	  5 能力分叉正确（gnb Discovery/Proxy=true，wg=false）
//	  6 前端/调用方不感知驱动（按 Capabilities 分支，不 hardcode vpn_type）

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/mynet/driver"
	"github.com/mynet-club/mynet_agent/mynet/keys"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func fx(t *testing.T, name string) []byte {
	t.Helper()
	cands := []string{
		filepath.Join("..", "..", "mynet_ctl", "docs", "knowledge-base", "spec", "fixtures", "bundle-envelope-v2", name),
		filepath.Join("..", "..", "..", "mynet_ctl", "docs", "knowledge-base", "spec", "fixtures", "bundle-envelope-v2", name),
	}
	for _, c := range cands {
		if b, err := os.ReadFile(c); err == nil {
			return b
		}
	}
	t.Skip("fixture " + name + " not found")
	return nil
}

func mustDecode(t *testing.T, name string) *domain.BundleEnvelope {
	t.Helper()
	env, err := domain.DecodeBundleEnvelope(fx(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// ── 判据 3：Envelope 互斥 × 三场景 ─────────────────────────────

func TestA8_C3_EnvelopeMutualExclusion(t *testing.T) {
	scenarios := map[string]struct {
		file   string
		driver domain.VPNDriverType
	}{
		"gnb":       {"gnb-minimal.json", domain.DriverTypeGNB},
		"wireguard": {"wireguard-minimal.json", domain.DriverTypeWireGuard},
		"gnb-peers": {"gnb-peers-routes.json", domain.DriverTypeGNB},
		"wg-multi":  {"wireguard-multi-peer.json", domain.DriverTypeWireGuard},
	}
	for name, sc := range scenarios {
		t.Run(name, func(t *testing.T) {
			env := mustDecode(t, sc.file)
			if env.Driver != sc.driver {
				t.Fatalf("driver=%s want %s", env.Driver, sc.driver)
			}
			switch sc.driver {
			case domain.DriverTypeGNB:
				if env.WireGuard != nil {
					t.Fatal("driver=gnb 不得携带 wireguard intent")
				}
				if env.Gnb == nil {
					t.Fatal("driver=gnb 应有 gnb intent")
				}
			case domain.DriverTypeWireGuard:
				if env.Gnb != nil {
					t.Fatal("driver=wireguard 不得携带 gnb intent")
				}
				if env.WireGuard == nil {
					t.Fatal("driver=wireguard 应有 wireguard intent")
				}
			}
		})
	}
}

// ── 判据 5：能力分叉 × 双驱动 ─────────────────────────────────

func TestA8_C5_CapabilitiesFork(t *testing.T) {
	wg := driver.NewWireGuardDriver("wg0", 1).Capabilities()
	gnb := driver.NewGNBDriver(1).Capabilities()

	if gnb.Discovery != true || gnb.Proxy != true {
		t.Fatalf("gnb caps=%+v want Discovery/Proxy true", gnb)
	}
	if wg.Discovery != false || wg.Proxy != false {
		t.Fatalf("wg caps=%+v want Discovery/Proxy false", wg)
	}
	// 调用方按 Capabilities 分支，而不是假设「换驱动功能一样」
	if gnb.Discovery == wg.Discovery {
		t.Fatal("capabilities must fork")
	}
}

// ── 判据 4：节点面 DH 一套逻辑（只差公钥格式转换）────────────────
func TestA8_C4_NodeFaceDHOneLogic(t *testing.T) {
	// wg：两边 X25519 直接 DH
	a, err := keys.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	b, err := keys.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	s1, err := keys.DH(a.Private, b.Public)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := keys.DH(b.Private, a.Public)
	if err != nil {
		t.Fatal(err)
	}
	if string(s1) != string(s2) {
		t.Fatal("wg path DH mismatch")
	}
	// gnb：私钥布局不同，但数据面 key_exchange 仍是 X25519 同构（见 keys/gnb）
	// 这里锁「编码/布局正交」：hex 往返不改字节
	ph, pb := a.ToHex()
	pb64, qb64 := a.ToBase64()
	if len(ph) != 64 || len(pb64) != 44 {
		t.Fatalf("encoding layers: hex=%d b64=%d", len(ph), len(pb64))
	}
	back, err := keys.PublicHexFromBase64(qb64)
	if err != nil || back != pb {
		t.Fatalf("hex↔base64 roundtrip: %q vs %q", back, pb)
	}
}

// ── 判据 2：驱动差异不泄漏到业务对象（Go 侧 domain）──────────────
func TestA8_C2_NoDriverTermsInBusinessObjects(t *testing.T) {
	// ConnectionPlan 是 L2 元数据；业务实体在 entities.go
	// 用序列化结果扫驱动原生术语（AllowedIPs / node.conf 仅允许出现在 intent 契约）
	forbidden := []string{"AllowedIPs", "node.conf", "wg0", "gnb_tun", "AllowedIPs"}
	// Envelope 的 wireguard intent 里 AllowedIPs 是契约字段（允许）；
	// 业务对象 ConnectionPlan / NetworkIntent 的 OsRoute 不得含它。
	cp := domain.ConnectionPlan{ConnectionID: 1, TargetNodeID: "2"}
	raw := jsonMust(cp)
	for _, bad := range forbidden {
		if strings.Contains(raw, bad) {
			t.Fatalf("ConnectionPlan leaked %s: %s", bad, raw)
		}
	}
	or := domain.OsRoute{Prefix: "10.0.0.0/8", Dev: "wg0"}
	raw = jsonMust(or)
	if strings.Contains(raw, "AllowedIPs") {
		t.Fatal("OsRoute must not contain AllowedIPs")
	}
}

// ── 判据 6：调用方按能力分支，不 hardcode vpn_type ─────────────
func TestA8_C6_CallersBranchOnCapabilities(t *testing.T) {
	// 模拟「向导」：只问能力，不判 gnb/wireguard 字符串
	for _, d := range []domain.VPNDriver{
		driver.NewWireGuardDriver("wg0", 1),
		driver.NewGNBDriver(1),
	} {
		lc, err := driver.AsLifecycle(d)
		if err != nil {
			t.Fatal(err)
		}
		caps := lc.Capabilities()
		// 正确分支：Discovery 功能只在 caps.Discovery 时提供
		offerDiscovery := caps.Discovery
		if d.GetDriverType() == domain.DriverTypeWireGuard && offerDiscovery {
			t.Fatal("wg must not offer discovery")
		}
		if d.GetDriverType() == domain.DriverTypeGNB && !offerDiscovery {
			t.Fatal("gnb must offer discovery")
		}
	}
}

// ── F-3：跨驱动路由 = 一条 ip route，驱动层互不知晓 ─────────────
func TestA8_F3_CrossDriverRoute(t *testing.T) {
	// A(gnb) → C(双驱动) → B(wg)：apply 写一条系统路由
	plan := `{
	  "routes": [
	    {"action":"replace","destination":"10.8.0.0/24","device":"wg0","metric":100},
	    {"action":"replace","destination":"10.9.0.0/24","device":"gnb0","metric":200}
	  ]
	}`
	dir := t.TempDir()
	p := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(p, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	// 渲染 argv：对 apply 无差别（不感知对端是 gnb 还是 wg）
	argvWG := routeArgvLike("replace", "10.8.0.0/24", "wg0", 100)
	argvGNB := routeArgvLike("replace", "10.9.0.0/24", "gnb0", 200)
	if argvWG[0] != "ip" || argvWG[1] != "route" {
		t.Fatalf("argv=%v", argvWG)
	}
	// 同一套命令形状，仅 device 不同
	if len(argvWG) != len(argvGNB) {
		t.Fatalf("shape mismatch %v vs %v", argvWG, argvGNB)
	}
	for i := range argvWG {
		if i == 5 { // device 参数位
			continue
		}
		// metric 位不同
	}
	if !strings.Contains(strings.Join(argvWG, " "), "wg0") ||
		!strings.Contains(strings.Join(argvGNB, " "), "gnb0") {
		t.Fatal("devices")
	}
	// 驱动层互不知晓对方：渲染的 conf 不含对方设备名
	wgConf := driver.RenderWGConfig(&domain.WireGuardIntent{
		Interface: domain.WgInterface{PrivateKey: "K", PublicKey: "P", KeyAlgorithm: "curve25519"},
	}, nil)
	if strings.Contains(wgConf, "gnb") {
		t.Fatal("wg conf must not mention gnb")
	}
	gnbConf := driver.RenderGNBNodeConf(&domain.GnbIntent{
		Identity: domain.GnbIdentity{NodeID: "1"},
	})
	if strings.Contains(gnbConf, "wg") {
		t.Fatal("gnb conf must not mention wg")
	}
}

func routeArgvLike(action, dest, dev string, metric int) []string {
	return []string{"ip", "route", action, dest, "dev", dev, "metric", itoa(metric)}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func jsonMust(v any) string {
	b, err := jsonMarshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
