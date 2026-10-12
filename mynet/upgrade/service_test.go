package upgrade

import (
	"strings"
	"testing"
)

func TestListOnlyDrivers(t *testing.T) {
	s := NewService("https://ctl.mynet.club")
	s.Fetch = func(string) ([]Artifact, error) {
		return []Artifact{
			{Component: ComponentMynet, Version: "1.0"},
			{Component: ComponentGNB, Version: "1.6.8"},
			{Component: ComponentWireGuard, Version: "1.0.0"},
		}, nil
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list=%v", list)
	}
	// mynet 自更新不进 driver 域
	for _, a := range list {
		if a.Component == ComponentMynet {
			t.Fatal("mynet must not appear in driver list")
		}
	}
}

func TestInstallRejectsMynet(t *testing.T) {
	s := NewService("https://ctl.mynet.club")
	if err := s.InstallDriver(ComponentMynet); err == nil {
		t.Fatal("mynet update 走 mynet update，不进 driver 域")
	}
}

func TestInstallDriver(t *testing.T) {
	s := NewService("https://ctl.mynet.club")
	installed := Component("")
	s.Fetch = func(string) ([]Artifact, error) {
		return []Artifact{{Component: ComponentGNB, Version: "1.6.8"}}, nil
	}
	s.Install = func(a Artifact) error {
		installed = a.Component
		return nil
	}
	if err := s.InstallDriver(ComponentGNB); err != nil {
		t.Fatal(err)
	}
	if installed != ComponentGNB {
		t.Fatalf("installed=%s", installed)
	}
	_ = s.Upgrade(ComponentWireGuard) // 索引无则报错，可接受
}

// 平台选择器：错平台不装、无匹配列候选（H4「选择器识别 platform」）。
func TestInstallDriverMatchesPlatform(t *testing.T) {
	host := HostPlatform()
	s := NewService("https://ctl.mynet.club")
	installed := ""
	s.Fetch = func(string) ([]Artifact, error) {
		return []Artifact{
			{Component: ComponentGNB, Version: "1.6.8", Platform: "openwrt/mipsel"},
			{Component: ComponentGNB, Version: "1.6.8", Platform: host},
		}, nil
	}
	s.Install = func(a Artifact) error {
		installed = a.Platform
		return nil
	}
	if err := s.InstallDriver(ComponentGNB); err != nil {
		t.Fatal(err)
	}
	if installed != host {
		t.Fatalf("装了错平台件 %q，应装 %q", installed, host)
	}

	// 全部不匹配 → 报错列候选，绝不装
	s.Fetch = func(string) ([]Artifact, error) {
		return []Artifact{{Component: ComponentGNB, Version: "1.6.8", Platform: "openwrt/mipsel"}}, nil
	}
	err := s.InstallDriver(ComponentGNB)
	if err == nil {
		t.Fatal("无本机平台件时必须报错")
	}
	if !strings.Contains(err.Error(), "openwrt/mipsel") {
		t.Fatalf("报错应列候选平台: %v", err)
	}
}
