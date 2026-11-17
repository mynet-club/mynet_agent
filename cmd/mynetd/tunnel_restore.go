package main

// 隧道开机恢复契约：
//
//	{ "want_running": true, "driver": "gnb", "node_id": "…", "updated_at": "…" }
//	（唯一读写口 = shared/tunnelstate；var/tunnel-state.json）
//
// 写者：执行器 `mynet service start|stop`（用户/CLI/面板意图）与 localapi 启停命令面。
// 读者：mynetd 启动 → 读开关与意图 → want_running 则经 `mynet service start` 拉起。
//
// B2 边界：mynetd **不做进程判活**（pid/kill -0 是系统能力，归执行器）——
// 「已在跑」问 `mynet service status`（退出码 0 = 在跑）；退出时**不覆写意图**
// （shutdown 顺序会把「用户想跑」误记成「没跑」，意图只由启停动作产生）。

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/mynet-club/mynet_agent/shared/tunnelstate"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

type agentCfg struct {
	AutoRestoreTunnel *bool `json:"auto_restore_tunnel"`
}

func agentCfgPath() string {
	return filepath.Join(utils.ConfDir(), "agent.json")
}

func autoRestoreEnabled() bool {
	raw, err := os.ReadFile(agentCfgPath())
	if err != nil {
		return true // 默认开
	}
	var c agentCfg
	if json.Unmarshal(raw, &c) != nil {
		return true
	}
	if c.AutoRestoreTunnel == nil {
		return true
	}
	return *c.AutoRestoreTunnel
}

// restoreTunnelIfNeeded mynetd 起来后：按意图拉起隧道。
// runningFn 报「已在跑」（执行器判活）；startFn 拉起（`mynet service start`，
// 自带先投影再起进程与装路由）。两者都由组合根注入，测试可换。
func restoreTunnelIfNeeded(runningFn func(driver string) bool, startFn func(driver string) error) {
	if !autoRestoreEnabled() {
		return
	}
	st := tunnelstate.Load()
	if !st.WantRunning {
		return
	}
	drv := st.Driver
	if drv == "" {
		drv = "gnb"
	}
	if runningFn != nil && runningFn(drv) {
		return // 已在跑
	}
	_ = startFn(drv)
}
