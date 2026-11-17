// Package tunnelstate 隧道启停意图（want_running）的唯一读写口。
//
// 职责边界（B2 / mynetd 纯通信）：
//   - 写者 = 执行器 `mynet service start|stop`（用户/CLI/面板的操作意图）与
//     localapi 的启停/接入命令面（markTunnelWantDriver 由此下沉而来）。
//   - 读者 = mynetd 开机恢复 restoreTunnelIfNeeded —— 意图从哪来恢复到哪去，
//     mynetd **不做进程判活**（pid/kill -0 是系统能力，归执行器）。
//   - mynetd 退出时**不覆写**意图：shutdown 顺序会把「用户想跑」误记成「没跑」。
package tunnelstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// State var/tunnel-state.json。
type State struct {
	WantRunning bool   `json:"want_running"`
	Driver      string `json:"driver,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// Path 状态文件位（var/，可丢缓存层，不进备份）。
func Path() string {
	return filepath.Join(utils.VarDir(), "tunnel-state.json")
}

// Load 读意图。读不到 = 零值（未记录过，不视为错误）。
func Load() State {
	var st State
	b, err := os.ReadFile(Path())
	if err != nil {
		return st
	}
	_ = json.Unmarshal(b, &st)
	return st
}

// Save 写意图（0600，原子替换）。
func Save(st State) error {
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// MarkWant 记录启停意图（合并已有 NodeID；driver 只在给了值时更新）。
func MarkWant(on bool, driver string) error {
	st := Load()
	st.WantRunning = on
	if driver != "" {
		st.Driver = driver
	}
	return Save(st)
}
