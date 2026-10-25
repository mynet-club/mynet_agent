package main

// 托盘有两个启动入口：启动台/应用程序里的 MyNet.app，以及 macOS launchd
// （Windows 是计划任务）登记的自启。两处都点过就会在菜单栏出现两个图标 ——
// fyne.io/systray 没有内建去重，节点状态却被两份守卫同时轮询。
//
// 这里用用户私有目录里的一把文件锁去重（与 tray-intent.json 同处，见
// intent.go：$MYNET_HOME/var 是 root 的，以登录用户跑的托盘写不进去）。
// 抢到锁的实例运行；抢不到的那位**不去争图标**，直接把已在跑的面板打开 ——
// 用户点第二下，意图本来就是「我要看面板」。
//
// 锁由内核在进程退出（含崩溃/被杀）时释放，所以不会留下陈旧锁文件把人
// 挡在外面；这也是不用 PID 文件 + 存活探测的原因（PID 会被复用，探测有竞态）。

import (
	"os"
	"path/filepath"
)

// trayLockPath 单实例锁文件路径。
func trayLockPath() (string, error) {
	dir, err := intentDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "tray.lock"), nil
}
