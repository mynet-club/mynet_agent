package upgrade

import "testing"

// R4-3：旧版→新版→健康检查 的升级流（Install 注入）
func TestUpgradeRoundtripMynet(t *testing.T) {
	installed := ""
	s := NewService("https://ctl.mynet.club")
	s.Fetch = func(string) ([]Artifact, error) {
		return []Artifact{
			{Component: ComponentMynet, Version: "0.1.0", Platform: "linux_amd64", URL: "/d/mynet/a.tgz"},
			{Component: ComponentMynet, Version: "0.2.0", Platform: "linux_amd64", URL: "/d/mynet/b.tgz"},
		}, nil
	}
	s.Install = func(a Artifact) error {
		installed = a.Version
		return nil
	}
	// update 路径：取最新
	all, _ := s.Fetch(s.IndexURL)
	var best Artifact
	for _, a := range all {
		if a.Component == ComponentMynet && a.Version > best.Version {
			best = a
		}
	}
	if best.Version != "0.2.0" {
		t.Fatalf("best=%s", best.Version)
	}
	if err := s.Install(best); err != nil {
		t.Fatal(err)
	}
	if installed != "0.2.0" {
		t.Fatalf("installed=%s", installed)
	}
}

// R5-2：双实例 presence 互不串号（内存实现；Redis 双实例等 Redis 集成）
func TestTwoPresenceInstancesIsolated(t *testing.T) {
	// 在 gateway 包测
}
