package mynet

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mynet-club/mynet_agent/shared"
)

// 反不变量：open-port 也是**规则本体**，不进 Go（2026-10-16 裁定）。
// 放行规则活在 nft 产物的 input 链（FirewallNftRules），由 LoadFirewallRules
// 委托系统加载器装。doctor 的机测是全 ruleset 扫，放自有表照样被认出来。
func TestFirewallArgvDoesNotComposeOpenPort(t *testing.T) {
	for _, caps := range []map[string]bool{
		{"nft": true, "iptables": true},
		{"iptables": true},
		{},
	} {
		snap := &shared.SystemSnapshot{Capabilities: caps}
		if got := firewallArgv(snap, FirewallOp{Action: "open-port", Proto: "udp", Port: 9016}); got != nil {
			t.Fatalf("open-port 不应由 Go 拼命令，got %v", got)
		}
		if got := firewallRollbackArgv(snap, FirewallOp{Action: "open-port", Proto: "udp", Port: 9016}); got != nil {
			t.Fatalf("open-port 同样无 Go 侧逆操作，got %v", got)
		}
	}
}

func TestConfListenPort(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]int{
		"listen 0.0.0.0:9016\n":   9016,
		"listen 9016\n":           9016,
		"listen [::]:51820\n":     51820,
		"nodeid 1\nlisten 1234\n": 1234,
	}
	for body, want := range cases {
		p := filepath.Join(dir, "node.conf")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := confListenPort(dir); got != want {
			t.Errorf("body=%q got=%d want=%d", body, got, want)
		}
	}
	if got := confListenPort(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("缺失 node.conf 应返回 0，实得 %d", got)
	}
}

func TestCheckFw4PersistenceMissing(t *testing.T) {
	// 本机（darwin/CI）通常没有 /etc/nftables.d/mynet.nft
	p := checkFw4Persistence(9016)
	if p == nil {
		t.Fatal("不应返回 nil")
	}
	// Exists 可真可假（取决于本机），但 HasRule 为 true 时文件必须存在
	if p.HasRule && !p.Exists {
		t.Fatal("HasRule 不能在文件不存在时为 true")
	}
	if !p.Exists && len(p.Notes) == 0 {
		t.Fatal("文件不存在时应给出修复提示")
	}
}

func TestFirewallDoctorUsage(t *testing.T) {
	// 非法参数应返回 2
	if code := cmdFirewallDoctor([]string{"--bogus"}); code != 2 {
		t.Fatalf("非法参数应返回 2，实得 %d", code)
	}
	if code := cmdFirewallDoctor([]string{"--port"}); code != 2 {
		t.Fatalf("--port 缺值应返回 2，实得 %d", code)
	}
}
