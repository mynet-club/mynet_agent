package upgrade

import "testing"

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
