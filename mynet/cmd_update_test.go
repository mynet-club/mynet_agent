package mynet

import (
	"runtime"
	"testing"

	"github.com/mynet-club/mynet_agent/mynet/upgrade"
)

func TestPickLatestMynetSemanticNotLexicographic(t *testing.T) {
	all := []upgrade.Artifact{
		{Platform: runtime.GOOS + "/" + runtime.GOARCH, Component: upgrade.ComponentMynet, Version: "0.9.0"},
		{Platform: runtime.GOOS + "/" + runtime.GOARCH, Component: upgrade.ComponentMynet, Version: "0.10.0"},
		{Platform: runtime.GOOS + "/" + runtime.GOARCH, Component: upgrade.ComponentMynet, Version: "0.2.0"},
		{Platform: runtime.GOOS + "/" + runtime.GOARCH, Component: upgrade.ComponentGNB, Version: "9.9.9"},
	}
	best := pickLatestMynet(all)
	if best == nil {
		t.Fatal("应挑出 mynet 组件")
	}
	// 字典序会挑 0.9.0；语义比较必须挑 0.10.0
	if best.Version != "0.10.0" {
		t.Fatalf("got %s want 0.10.0（字典序陷阱）", best.Version)
	}
}

func TestPickLatestMynetEmpty(t *testing.T) {
	if pickLatestMynet(nil) != nil {
		t.Fatal("空索引应返回 nil")
	}
	if pickLatestMynet([]upgrade.Artifact{{Platform: runtime.GOOS + "/" + runtime.GOARCH, Component: upgrade.ComponentGNB, Version: "1"}}) != nil {
		t.Fatal("无 mynet 组件应返回 nil")
	}
}

func TestPickLatestMynetPrerelease(t *testing.T) {
	all := []upgrade.Artifact{
		{Platform: runtime.GOOS + "/" + runtime.GOARCH, Component: upgrade.ComponentMynet, Version: "v0.0.0-e831eb8-dirty"},
		{Platform: runtime.GOOS + "/" + runtime.GOARCH, Component: upgrade.ComponentMynet, Version: "v0.1.0"},
	}
	best := pickLatestMynet(all)
	if best == nil || best.Version != "v0.1.0" {
		t.Fatalf("正式版应高于预发布，got %+v", best)
	}
}

func TestCmdUpdateUsage(t *testing.T) {
	if code := cmdUpdate([]string{"--bogus"}); code != 2 {
		t.Fatalf("非法参数应返回 2，实得 %d", code)
	}
}

func TestNormalizeSum(t *testing.T) {
	if got := normalizeSum(" ABCDEF "); got != "abcdef" {
		t.Fatalf("got %q", got)
	}
}

func TestPickLatestMynetOnlyMatchingPlatform(t *testing.T) {
	all := []upgrade.Artifact{
		{Component: upgrade.ComponentMynet, Version: "v9.0.0", Platform: "linux/arm64"},
		{Component: upgrade.ComponentMynet, Version: "v2.0.0", Platform: "darwin/amd64"},
		{Component: upgrade.ComponentMynet, Version: "v0.1.0", Platform: "darwin/arm64"},
		{Component: upgrade.ComponentMynet, Version: "v0.1.1", Platform: "darwin/arm64"},
		{Component: upgrade.ComponentMynet, Version: "v99.0.0"},
	}
	// 自更新只要 archive；这些用例未标 ext 时按「可自更新」处理（向后兼容）
	for i := range all {
		if all[i].Ext == "" {
			all[i].Ext = "tgz"
			all[i].Kind = "archive"
		}
	}
	got := pickLatestMynetForPlatform(all, "darwin/arm64")
	if got == nil || got.Version != "v0.1.1" {
		t.Fatalf("wrong target: %+v", got)
	}
	if pickLatestMynetForPlatform(all, "windows/amd64") != nil {
		t.Fatal("must reject missing platform")
	}
}

// G4-C / CPI-76：自更新不得选 installer（exe）；OpenWrt 不得落到 linux 裸包。
func TestPickLatestSkipsInstallerAndOpenWrtNotLinux(t *testing.T) {
	all := []upgrade.Artifact{
		{Component: upgrade.ComponentMynet, Version: "v1.0.0", Platform: "windows/amd64", Ext: "exe", Kind: "installer"},
		{Component: upgrade.ComponentMynet, Version: "v1.0.0", Platform: "windows/amd64", Ext: "zip", Kind: "archive"},
		{Component: upgrade.ComponentMynet, Version: "v1.0.0", Platform: "linux/amd64", Ext: "tgz", Kind: "archive"},
		{Component: upgrade.ComponentMynet, Version: "v1.0.0", Platform: "openwrt/mipsel", Ext: "ipk", Kind: "installer"},
	}
	w := pickLatestMynetForPlatform(all, "windows/amd64")
	if w == nil || w.Ext != "zip" {
		t.Fatalf("windows 自更新应选 zip 而非 exe: %+v", w)
	}
	if pickLatestMynetForPlatform(all, "openwrt/mipsel") != nil {
		t.Fatal("openwrt 只有 ipk 装机件，自更新不得硬选（宁缺勿装错）")
	}
	l := pickLatestMynetForPlatform(all, "linux/amd64")
	if l == nil || l.Platform != "linux/amd64" {
		t.Fatal("linux 选自己的 tgz")
	}
}
func TestPickLatestMynetMipsAlias(t *testing.T) {
	all := []upgrade.Artifact{{
		Component: upgrade.ComponentMynet, Version: "v0.1.1",
		Platform: "linux/mipsel", Ext: "tgz", Kind: "archive",
	}}
	// HostPlatform 把 mipsle 归一成 mipsel 后再匹配
	if pickLatestMynetForPlatform(all, "linux/mipsel") == nil {
		t.Fatal("ctl mipsel alias required")
	}
}
