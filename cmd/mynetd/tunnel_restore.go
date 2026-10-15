package main

// 隧道随 mynetd 恢复 —— 配置项 + 退出时记住 gnb|wg 状态。
//
//	$MYNET_HOME/var/tunnel-state.json
//	  { "want_running": true, "driver": "gnb", "node_id": "…", "updated_at": "…" }
//
//	/conf/agent.json
//	  { "auto_restore_tunnel": true }     ← 总开关（默认 true）
//
// 行为：
//   mynetd 启动 → 读开关与上次状态 → want_running 则拉起 gnb|wg
//   mynetd 退出 → 看**当前** gnb|wg 是否在跑，写回 state
// 用户启停（面板/托盘）会改 want_running，所以下次开机跟着上次意图走。

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

type tunnelState struct {
	WantRunning bool   `json:"want_running"`
	Driver      string `json:"driver,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

type agentCfg struct {
	AutoRestoreTunnel *bool `json:"auto_restore_tunnel"`
}

func tunnelStatePath() string {
	return filepath.Join(utils.VarDir(), "tunnel-state.json")
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

func loadTunnelState() tunnelState {
	raw, err := os.ReadFile(tunnelStatePath())
	if err != nil {
		return tunnelState{}
	}
	var s tunnelState
	_ = json.Unmarshal(raw, &s)
	return s
}

func saveTunnelState(s tunnelState) error {
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(tunnelStatePath(), append(b, '\n'), 0o600)
}

// gnbPidFile 当前投影目录的 gnb.pid。
// D4-E：节点唯一存放位 conf/<form>/<nodeId>/；旧 driver/gnb/conf 只作读回退。
func gnbPidFile() string {
	form, nodeID := utils.ActiveNodeInfo()
	if nodeID != "" {
		if dir, err := utils.NodeRuntimePath(form, nodeID); err == nil {
			if fileExists(filepath.Join(dir, "gnb.pid")) || fileExists(filepath.Join(dir, "node.conf")) {
				return filepath.Join(dir, "gnb.pid")
			}
		}
	}
	base := filepath.Join(utils.GetMyNetHome(), "driver", "gnb", "conf")
	if ents, err := os.ReadDir(base); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				return filepath.Join(base, e.Name(), "gnb.pid")
			}
		}
	}
	return filepath.Join(utils.VarDir(), "gnb", "gnb.pid")
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func gnbRunning() bool {
	b, err := os.ReadFile(gnbPidFile())
	if err != nil {
		return false
	}
	pidStr := strings.TrimSpace(string(b))
	if pidStr == "" {
		return false
	}
	// kill -0
	cmd := exec.Command("kill", "-0", pidStr)
	return cmd.Run() == nil
}

// recordTunnelExit mynetd 退出前：把**当前** gnb|wg 状态写回。
func recordTunnelExit() {
	st := loadTunnelState()
	st.WantRunning = gnbRunning()
	if st.Driver == "" {
		st.Driver = "gnb"
	}
	_ = saveTunnelState(st)
}

// restoreTunnelIfNeeded mynetd 起来后：按上次状态拉起。
func restoreTunnelIfNeeded(startFn func(driver string) error) {
	if !autoRestoreEnabled() {
		return
	}
	st := loadTunnelState()
	if !st.WantRunning {
		return
	}
	if gnbRunning() {
		return // 已在跑
	}
	drv := st.Driver
	if drv == "" {
		drv = "gnb"
	}
	_ = startFn(drv)
	// 再确认写回
	st.Driver = drv
	st.WantRunning = true
	_ = saveTunnelState(st)
}
