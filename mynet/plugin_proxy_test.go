package mynet

// proxy 插件：出站选点、proxy.conf 投影、gnb 能力探测。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent-V2/shared/domain"
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
		Outbound: domain.PluginProxyOutbound{Mode: "rule", Via: "tun"},
		Exits: []domain.ProxyExit{
			{NodeID: "3002", Weight: 20, Order: 1, Enabled: true},
			{NodeID: "3003", Weight: 5, Enabled: true},
		},
	}
	s := RenderGnbProxyConf(c)
	for _, want := range []string{"enabled on", "exit 1|3002|20", "via tun"} {
		if !strings.Contains(s, want) {
			t.Fatalf("proxy.conf 缺 %q:\n%s", want, s)
		}
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
	// address.conf 应把 3002 标成 f（需要有 endpoint 才进 StaticNodes；
	// 无 endpoint 时进 ForwardNodes，node.conf 仍应 proxy on）
	nc, _ := os.ReadFile(filepath.Join(root, "driver", "gnb", "conf", "3001", "node.conf"))
	if !strings.Contains(string(nc), "proxy on") {
		t.Fatalf("启用 proxy 插件后 node.conf 应有 proxy on:\n%s", nc)
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
