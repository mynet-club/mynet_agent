package main

import "testing"

import "github.com/mynet-club/mynet_agent/shared/utils"

// 锁「托盘启停目标 = install 注册名」（C1-S3）。
//
// 历史漂移：托盘 macOS 打 `club.mynet.mynetd`（install 注册的是
// `com.mynet.mynetd`）、Windows 打 schtasks `MyNetd`（install 已改 SCM
// `mynetd`）——启停目标指向 install 从不注册的标识，按钮是摆设。
//
// 现在两侧共用 shared/utils/service_names.go 的常量；本 fixture 对每个
// GOOS 断言 Controller 的目标就是 install 的注册名（服务名矩阵
// lifecycle-contract §3：linux systemd `mynetd`+`mynet_gnb`、darwin
// launchd `com.mynet.mynetd`、windows SCM `mynetd`）。
func TestControllerTargetsMatchInstallRegistration(t *testing.T) {
	cases := []struct {
		goos     string
		describe string
	}{
		{"darwin", "launchd/" + utils.LaunchdLabelMynetd},
		{"windows", "scm/" + utils.ServiceNameMynetd},
		{"linux", "systemd/" + utils.ServiceNameMynetd},
		{"openwrt", "systemd/" + utils.ServiceNameMynetd},
	}
	for _, c := range cases {
		got := newController(c.goos, "").Describe()
		if got != c.describe {
			t.Errorf("GOOS=%s Describe()=%q，install 注册名目标应为 %q", c.goos, got, c.describe)
		}
	}
}

// 服务名 SSOT 本身要与合同矩阵一致（上游锁，见 shared/utils 的同名断言）。
func TestServiceIdentifiersAreContractNames(t *testing.T) {
	if utils.LaunchdLabelMynetd != "com.mynet.mynetd" {
		t.Errorf("LaunchdLabelMynetd=%q，install 注册的是 com.mynet.mynetd", utils.LaunchdLabelMynetd)
	}
	if utils.ServiceNameMynetd != "mynetd" {
		t.Errorf("ServiceNameMynetd=%q，SCM/systemd 注册的是 mynetd", utils.ServiceNameMynetd)
	}
	if utils.ServiceNameGNB != "mynet_gnb" {
		t.Errorf("ServiceNameGNB=%q，systemd 注册的是 mynet_gnb", utils.ServiceNameGNB)
	}
}
