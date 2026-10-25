package mynet

import (
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared"
)

func pcSnap(osName string) *shared.SystemSnapshot {
	return &shared.SystemSnapshot{OS: osName, Capabilities: map[string]bool{}}
}

func TestPCProxyPinCmdsDarwinDialect(t *testing.T) {
	snap := pcSnap("darwin")
	cmds := PCProxyPinCmds(snap, []string{"1.2.3.4", "5.6.7.8", "not-an-ip"}, "utun6")
	// darwin 的 replace = 先删后加 → 每 IP 2 条；非法 IP 跳过。
	if len(cmds) != 4 {
		t.Fatalf("应生成 4 条（2 IP × 删+加；非法 IP 跳过），实际 %d", len(cmds))
	}
	text := joinCmds(cmds)
	for _, want := range []string{"1.2.3.4", "5.6.7.8", "utun6"} {
		if !strings.Contains(text, want) {
			t.Fatalf("darwin 方言缺 %s:\n%s", want, text)
		}
	}
}

func TestPCProxyPinNoopOnLinux(t *testing.T) {
	if cmds := PCProxyPinCmds(pcSnap("linux"), []string{"1.2.3.4"}, "gnb_tun"); len(cmds) != 0 {
		t.Fatalf("Linux 不得在主线钉主表路由: %v", cmds)
	}
}

func TestPCProxyPinDelOptional(t *testing.T) {
	cmds := PCProxyPinDelCmds(pcSnap("windows"), []string{"1.2.3.4"}, "P2PNet")
	if len(cmds) == 0 {
		t.Fatal("windows 应生成删除命令")
	}
	for _, c := range cmds {
		if !c.optional {
			t.Fatalf("删除命令应 optional（幂等）: %v", c.argv)
		}
	}
}

func TestPCProxyPinNeverTouchesDefaultRoute(t *testing.T) {
	// 路由表安全不变量：钉扎方言只产出主机路由，绝无 default / 0.0.0.0。
	// darwin 用 `route -n -host <ip>`（裸 IP，不带 /32）；windows 用 `prefix=<ip>/32`。
	for _, osName := range []string{"darwin", "windows"} {
		cmds := PCProxyPinCmds(pcSnap(osName), []string{"1.2.3.4", "8.8.8.8"}, "utun4")
		text := joinCmds(cmds)
		if strings.Contains(text, "default") {
			t.Fatalf("%s: 钉扎不得含 default:\n%s", osName, text)
		}
		if strings.Contains(text, "0.0.0.0/0") {
			t.Fatalf("%s: 钉扎不得含 0.0.0.0/0:\n%s", osName, text)
		}
		if !strings.Contains(text, "1.2.3.4") {
			t.Fatalf("%s: 应含目标 IP:\n%s", osName, text)
		}
		switch osName {
		case "darwin":
			if !strings.Contains(text, "-host") {
				t.Fatalf("darwin 应为 -host 主机路由:\n%s", text)
			}
		case "windows":
			if !strings.Contains(text, "prefix=1.2.3.4/32") {
				t.Fatalf("windows 应为 prefix=/32:\n%s", text)
			}
		}
	}
}

func TestMergeIPsToCIDRMinimal(t *testing.T) {
	// 相邻成对 → /31；四个连续 → /30；不相邻保持 /32；绝不扩大覆盖
	got := MergeIPsToCIDR([]string{"1.2.3.4", "1.2.3.5", "1.2.3.6", "1.2.3.7", "9.9.9.9"})
	want := "1.2.3.4/30,9.9.9.9/32"
	if strings.Join(got, ",") != want {
		t.Fatalf("merge: got %v want %s", got, want)
	}
	// 半对齐：.5+.6 不能合成一个块（缺 .4/.7），保持两个 /32
	got2 := MergeIPsToCIDR([]string{"1.2.3.5", "1.2.3.6"})
	if strings.Join(got2, ",") != "1.2.3.5/32,1.2.3.6/32" {
		t.Fatalf("非对齐不合并: %v", got2)
	}
	// 空
	if MergeIPsToCIDR(nil) != nil {
		t.Fatal("空集应 nil")
	}
}

func TestHookDomainStarMatch(t *testing.T) {
	// *.example.com 归一化后 = example.com，后缀匹配子域
	r := &dnsHookRules{
		proxied: map[string]bool{"example.com": true},
	}
	if !r.shouldPin("example.com") || !r.shouldPin("a.example.com") || !r.shouldPin("a.b.example.com") {
		t.Fatal("*.example.com 应匹配自身与子域")
	}
	if r.shouldPin("notexample.com") {
		t.Fatal("后缀误匹配")
	}
}
