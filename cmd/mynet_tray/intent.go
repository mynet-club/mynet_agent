package main

// 托盘记住用户上次意图：节点是否应在跑。
// 落在 $MYNET_HOME/var/tray-intent.json（可丢，不是节点身份）。
//
// 逻辑（用户约定）：
//   托盘启动 → 先拉 mynetd → 读 last_intent → 要跑则再起节点

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/mynet-club/mynet_agent-V2/shared/utils"
)

type trayIntent struct {
	// WantNode 上次用户是否让节点在跑。
	WantNode bool `json:"want_node"`
}

func intentPath() string {
	return filepath.Join(utils.VarDir(), "tray-intent.json")
}

func loadIntent() trayIntent {
	raw, err := os.ReadFile(intentPath())
	if err != nil {
		return trayIntent{}
	}
	var t trayIntent
	_ = json.Unmarshal(raw, &t)
	return t
}

func saveIntent(t trayIntent) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(intentPath(), append(b, '\n'), 0o600)
}
