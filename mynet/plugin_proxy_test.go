package mynet

// proxy 插件：出站选点、proxy.conf 投影、gnb 能力探测。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

func TestEffectiveExitsSortAndCap(t *testing.T) {
	c := &domain.PluginProxyConfig{
		MaxExits: 2,
		Exits: []domain.ProxyExit{
			{NodeID: "c", Weight: 5, Order: 3, Enabled: true},
			{NodeID: "a", Weight: 1, Order: 1, Enabled: true},
			{NodeID: "b", Weight: 9, Order: 2, Enabled: true},
			{NodeID: "off", Weight: 99, Enabled: false},
		},
	}
	got := c.EffectiveExits()
	if len(got) != 2 {
		t.Fatalf("MaxExits=2 应截断: %+v", got)
	}
	if got[0].NodeID != "a" || got[1].NodeID != "b" {
		t.Fatalf("应按 Order: %+v", got)
	}
}

func TestRenderGnbProxyConf(t *testing.T) {
	c := &domain.PluginProxyConfig{
		Role:     "client",
		Outbound: domain.PluginProxyOutbound{Mode: "rule", Via: "tun"},
		Exits: []domain.ProxyExit{
			{NodeID: "3002", Weight: 20, Order: 1, Enabled: true},
			{NodeID: "3003", Weight: 5, Enabled: true}, // order=0 合法最低层
		},
		Groups: []domain.ProxyGroup{
			{
				Name:    "dns",
				Targets: []string{"8.8.8.0/24", "0.0.0.0/0"}, // /0 必须被跳过
				Exits:   []domain.ProxyExit{{NodeID: "3003", Weight: 20, Order: 1, Enabled: true}},
			},
		},
	}
	s := RenderGnbProxyConf(c)

	// P2 真格式（p2-route-proxy TEST-REPORT §2）
	for _, want := range []string{
		"role client",
		"proxy on",
		"[default]",
		"exit 3002 w=20 order=1",
		"exit 3003 w=5", // order=0 不写 order=
		"[group dns]",
		"target 8.8.8.0/24",
		"exit 3003 w=20 order=1",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("proxy.conf 缺 %q:\n%s", want, s)
		}
	}
	// 反例：P2 不认的键/格式
	for _, bad := range []string{"mode rule", "via tun", "max_exits", "exit 1|", "0.0.0.0/0"} {
		if strings.Contains(s, bad) {
			t.Fatalf("proxy.conf 不该含 %q:\n%s", bad, s)
		}
	}
	// client 不写 ingress_open
	if strings.Contains(s, "ingress_open") {
		t.Fatalf("client 不该写 ingress_open:\n%s", s)
	}
}

func TestRenderGnbProxyConfServerRole(t *testing.T) {
	s := RenderGnbProxyConf(&domain.PluginProxyConfig{Role: "server"})
	if !strings.Contains(s, "role server") || !strings.Contains(s, "ingress_open on") {
		t.Fatalf("server 应写 ingress_open on:\n%s", s)
	}
}

func TestApplyProxyToGnbRespectsCapability(t *testing.T) {
	old := GNBProxySupported
	defer func() { GNBProxySupported = old }()

	dir := t.TempDir()
	c := &domain.PluginProxyConfig{Exits: []domain.ProxyExit{{NodeID: "1", Enabled: true}}}

	// 不支持 → 不写、有警告
	GNBProxySupported = func() (bool, string) { return false, "无 P2" }
	written, warn, err := ApplyProxyToGnb(dir, c, false)
	if err != nil || written {
		t.Fatalf("written=%v err=%v", written, err)
	}
	if !strings.Contains(warn, "不支持") {
		t.Fatalf("warn=%q", warn)
	}
	if _, err := os.Stat(filepath.Join(dir, "proxy.conf")); err == nil {
		t.Fatal("不支持时不该写 proxy.conf")
	}

	// 支持 → 写入
	GNBProxySupported = func() (bool, string) { return true, "ok" }
	written, _, err = ApplyProxyToGnb(dir, c, false)
	if err != nil || !written {
		t.Fatalf("written=%v err=%v", written, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "proxy.conf"))
	if err != nil || !strings.Contains(string(b), "exit ") {
		t.Fatalf("proxy.conf: %v %s", err, b)
	}
}

func TestApplyActiveWritesProxyConfWhenPluginEnabled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	old := GNBProxySupported
	GNBProxySupported = func() (bool, string) { return true, "ok" }
	defer func() { GNBProxySupported = old }()

	if rc := cmdSetupV2([]string{"--local", "--node-id", "3001", "--overlay", "10.0.0.1/32"}); rc != 0 {
		t.Fatal(rc)
	}
	if rc := configFaceKeysGenerate([]string{"--vpn", "gnb", "--node-id", "3001"}); rc != 0 {
		t.Fatal(rc)
	}
	if rc := cmdPlugin([]string{"install", "proxy"}); rc != 0 {
		t.Fatal(rc)
	}
	if rc := cmdPlugin([]string{"exits", "add", "3002", "--weight", "20", "--order", "1"}); rc != 0 {
		t.Fatal(rc)
	}
	if rc := cmdPlugin([]string{"enable", "proxy"}); rc != 0 {
		t.Fatal(rc)
	}
	if err := ApplyFromActive(false); err != nil {
		t.Fatal(err)
	}

	proxyConf := filepath.Join(root, "driver", "gnb", "conf", "3001", "proxy.conf")
	b, err := os.ReadFile(proxyConf)
	if err != nil {
		t.Fatalf("缺 proxy.conf: %v", err)
	}
	if !strings.Contains(string(b), "3002") {
		t.Fatalf("proxy.conf 应含出口 3002:\n%s", b)
	}
	// address.conf **不得**把出口标成 f（出口=数据目标，非 mesh 转发）
	acPath := filepath.Join(root, "driver", "gnb", "conf", "3001", "address.conf")
	if ac, err := os.ReadFile(acPath); err == nil {
		for _, line := range strings.Split(string(ac), "\n") {
			if strings.HasPrefix(line, "f|") {
				t.Fatalf("address.conf 不得写 f 角色:\n%s", ac)
			}
		}
	}
	// node.conf **不得**出现 proxy on：gnb proxy 开关归独立 proxy.conf
	nc, _ := os.ReadFile(filepath.Join(root, "driver", "gnb", "conf", "3001", "node.conf"))
	if strings.Contains(string(nc), "proxy on") {
		t.Fatalf("node.conf 不该有 proxy on（应只在 proxy.conf）:\n%s", nc)
	}
}

func TestProbeGNBProxyNoBinary(t *testing.T) {
	// 用「无 Features 行」的 gnb 模拟旧版；不要依赖 PATH 上没有 gnb
	//（本机可能装了真 gnb，findGnbBin 会找到它）。
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", filepath.Join(dir, "home"))
	gnbDir := filepath.Join(dir, "home", "driver", "gnb", "bin")
	if err := os.MkdirAll(gnbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gnbDir, "gnb"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	ok, reason := probeGNBProxySupport()
	if ok {
		t.Fatalf("无 Features 不应报支持: %s", reason)
	}
	if reason == "" {
		t.Fatal("应给出原因")
	}
}

func TestProbeGNBProxyParsesFeaturesLine(t *testing.T) {
	// 假 gnb 放在 MYNET_HOME/driver/gnb/bin（探测优先路径），避免系统 gnb 干扰
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", filepath.Join(dir, "home"))
	gnbDir := filepath.Join(dir, "home", "driver", "gnb", "bin")
	if err := os.MkdirAll(gnbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(gnbDir, "gnb")
	script := "#!/bin/sh\necho 'GNB 1.6.8'\necho 'Features: proxy'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())

	ok, reason := probeGNBProxySupport()
	if !ok {
		t.Fatalf("应识别 Features: proxy —— %s", reason)
	}

	// 无 proxy 的 Features
	script2 := "#!/bin/sh\necho 'Features: hot-reload'\n"
	_ = os.WriteFile(fake, []byte(script2), 0o755)
	ok, reason = probeGNBProxySupport()
	if ok {
		t.Fatalf("Features 无 proxy 不应支持: %s", reason)
	}
	if !strings.Contains(reason, "无 proxy") {
		t.Fatalf("reason=%q", reason)
	}

	// 完全没有 Features 行
	_ = os.WriteFile(fake, []byte("#!/bin/sh\necho hi\n"), 0o755)
	ok, reason = probeGNBProxySupport()
	if ok || !strings.Contains(reason, "Features") {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
}

// 现网 gnb（p2 只有 0001-proxy-conf-v3，无 Features 行）必须被认成支持 ——
// 旧探测只认 0005 的 Features 行，会把打了 P2 的构建误判成没打。
func TestProbeGNBProxyFallbackMarker(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", filepath.Join(dir, "home"))
	gnbDir := filepath.Join(dir, "home", "driver", "gnb", "bin")
	if err := os.MkdirAll(gnbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'GNB 1.6.8'\n# proxy.conf loaded gen=%llu\n"
	if err := os.WriteFile(filepath.Join(gnbDir, "gnb"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())

	ok, reason := probeGNBProxySupport()
	if !ok {
		t.Fatalf("含 mark_p2 行为字符串应判支持: %s", reason)
	}
}

// Features 行存在但不含 proxy 是明确否定，不得被兜底翻案。
func TestProbeGNBProxyFeaturesLineDenies(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", filepath.Join(dir, "home"))
	gnbDir := filepath.Join(dir, "home", "driver", "gnb", "bin")
	if err := os.MkdirAll(gnbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'Features: zip'\n# proxy.conf loaded gen=%llu\n"
	if err := os.WriteFile(filepath.Join(gnbDir, "gnb"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())

	ok, reason := probeGNBProxySupport()
	if ok {
		t.Fatalf("Features 行无 proxy 必须判不支持（兜底不得翻案）: %s", reason)
	}
}

func TestProvisionProxyPlugin(t *testing.T) {
	home := t.TempDir()
	created, err := provisionProxyPlugin(home)
	if err != nil || !created {
		t.Fatalf("首次应置备: created=%v err=%v", created, err)
	}
	mf := filepath.Join(home, "plugins", domain.PluginNameProxy, "manifest.json")
	data, err := os.ReadFile(mf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), domain.PluginNameProxy) || !strings.Contains(string(data), "p2-route-proxy") {
		t.Fatalf("manifest 内容应含插件名与补丁栈说明:\n%s", data)
	}
	created, err = provisionProxyPlugin(home)
	if created || err != nil {
		t.Fatalf("复跑应幂等不动: created=%v err=%v", created, err)
	}
}
