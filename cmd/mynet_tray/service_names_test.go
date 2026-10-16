package main

import "testing"

import "github.com/mynet-club/mynet_agent/shared/utils"

// 服务名 SSOT 本身要与合同矩阵一致（上游锁，见 shared/utils 的同名断言）。
// linux systemd `mynetd`+`mynet_gnb`、darwin launchd `com.mynet.mynetd`、
// windows SCM `mynetd`。
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
