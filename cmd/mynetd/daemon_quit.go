package main

// daemonQuit —— 托盘「退出应用」：停隧道 + 注销并退出 mynetd。
// 2026-10-28 用户裁定：普通用户退出托盘 = 退出整个应用，不留后台隧道/服务。
// 平台：darwin launchctl bootout / linux systemctl stop / windows sc stop。

import (
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// newDaemonQuit 返回注入 localapi 的退出闭包。
// 注意：清理「想要隧道」意图由 localapi 侧 markTunnelWantDriver 负责。
func newDaemonQuit(stopTunnel func()) func() error {
	return func() error {
		if stopTunnel != nil {
			stopTunnel()
		}
		// 异步退出：先让 HTTP 200 回到托盘，再停服务/退出。
		go func() {
			time.Sleep(250 * time.Millisecond)
			stopSelfService()
		}()
		return nil
	}
}

// stopSelfService 停掉自身系统服务（KeepAlive/SCM 场景必须走服务停止，
// 裸 os.Exit 会被 launchd/sc 拉起或留下半死服务）。
func stopSelfService() {
	name := utils.ServiceNameMynetd
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "bootout", "system/"+utils.LaunchdLabelMynetd).CombinedOutput()
		logger.Info("launchctl bootout mynetd", "err", err, "out", string(out))
	case "linux":
		if out, err := exec.Command("systemctl", "stop", name).CombinedOutput(); err != nil {
			logger.Debug("systemctl stop mynetd", "err", err, "out", string(out))
		}
		// procd/OpenWrt 无 systemd 时：直接退，由 init 按需拉起策略决定
	case "windows":
		// SCM Stop → winService.Execute 走 StopPending 清洁收尾
		if out, err := exec.Command("sc.exe", "stop", name).CombinedOutput(); err != nil {
			logger.Debug("sc stop mynetd", "err", err, "out", string(out))
		}
	}
	logger.Info("mynetd 退出（用户关闭应用）")
	// 服务停止是异步的；兜底硬退，避免 HTTP 处理线程挂着。
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}
