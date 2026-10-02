package mynet

import (
	"testing"

	"github.com/mynet-club/mynet_agent/mynet/upgrade"
)

func TestPickLatestMynetSemanticNotLexicographic(t *testing.T) {
	all := []upgrade.Artifact{
		{Component: upgrade.ComponentMynet, Version: "0.9.0"},
		{Component: upgrade.ComponentMynet, Version: "0.10.0"},
		{Component: upgrade.ComponentMynet, Version: "0.2.0"},
		{Component: upgrade.ComponentGNB, Version: "9.9.9"},
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
	if pickLatestMynet([]upgrade.Artifact{{Component: upgrade.ComponentGNB, Version: "1"}}) != nil {
		t.Fatal("无 mynet 组件应返回 nil")
	}
}

func TestPickLatestMynetPrerelease(t *testing.T) {
	all := []upgrade.Artifact{
		{Component: upgrade.ComponentMynet, Version: "v0.0.0-e831eb8-dirty"},
		{Component: upgrade.ComponentMynet, Version: "v0.1.0"},
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
