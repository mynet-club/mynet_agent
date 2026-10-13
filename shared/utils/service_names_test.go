package utils

import "testing"

// 服务登记标识必须与 lifecycle-contract §3 服务名矩阵一致。
//
// 这条 fixture 锁的是「托盘启停目标 = install 注册名」的公共上游：
// mynet install（cmd_install_launchd.go / cmd_install_win.go）与
// cmd/mynet_tray/control.go 都引用本组常量，任一侧改成自己的字面量
// 都会在这里或 tray 侧的 control_test.go 红。
func TestServiceNamesMatchLifecycleContract(t *testing.T) {
	cases := map[string]struct{ got, want string }{
		"darwin LaunchDaemon label": {LaunchdLabelMynetd, "com.mynet.mynetd"},
		"gnb legacy label":          {LaunchdLabelGNB, "com.mynet.gnb"},
		"tray LaunchAgent label":    {LaunchdLabelTray, "club.mynet.tray"},
		"systemd/SCM/procd 名":       {ServiceNameMynetd, "mynetd"},
		"gnb systemd 单元名":           {ServiceNameGNB, "mynet_gnb"},
	}
	for name, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q，合同 = %q（lifecycle-contract §3）", name, c.got, c.want)
		}
	}
}
