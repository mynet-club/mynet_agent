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
	got := pickLatestMynetForPlatform(all, "darwin", "arm64")
	if got == nil || got.Version != "v0.1.1" {
		t.Fatalf("wrong target: %+v", got)
	}
	if pickLatestMynetForPlatform(all, "windows", "amd64") != nil {
		t.Fatal("must reject missing platform")
	}
}
func TestPickLatestMynetMipsAlias(t *testing.T) {
	all := []upgrade.Artifact{{Component: upgrade.ComponentMynet, Version: "v0.1.1", Platform: "linux/mipsel"}}
	if pickLatestMynetForPlatform(all, "linux", "mipsle") == nil {
		t.Fatal("ctl mipsel alias required")
	}
}
