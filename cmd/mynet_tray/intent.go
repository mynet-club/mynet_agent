package main

// 托盘记住用户上次意图：节点是否应在跑。
//
// 落点是**登录用户的私有配置目录**（os.UserConfigDir()/MyNet/），不是
// $MYNET_HOME/var。后者是 root 的系统级目录 —— main.go:41-50 在 darwin/linux
// 上把 MYNET_HOME 定成 /opt/mynet，utils.PlatformDefaultHome 的注释自陈
// 「系统级唯一，不设用户级模式：节点守护要建 TUN/写路由/持身份密钥」，
// 而全仓零 chown ⇒ 以登录用户身份跑的托盘**根本写不进去**。
// 旧版还把 saveIntent 的错误 `_ =` 丢掉，于是「记住上次意图」一直静默失效：
// 用户点了连接、重启托盘、没恢复，也看不到任何原因（D9）。
//
// 意图是可丢的偏好、不是节点身份，放用户目录反而更对：每个登录用户各自一份，
// 互不干扰，也不需要 root。
//
// 无自动迁移：旧的 $MYNET_HOME/var/tray-intent.json 不读也不删（托盘本来也
// 读不到 root 目录里那个）；新位置缺文件按既有默认 false。

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type trayIntent struct {
	// WantNode 上次用户是否让节点在跑。
	WantNode bool `json:"want_node"`
}

// intentDir 用户私有配置目录下的 MyNet/。
// os.UserConfigDir：darwin=$HOME/Library/Application Support、
// linux=$XDG_CONFIG_HOME 或 $HOME/.config、windows=%AppData%。
func intentDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "MyNet"), nil
}

func intentPath() (string, error) {
	dir, err := intentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tray-intent.json"), nil
}

// loadIntent 读不到 / 读不懂一律按默认（false）。意图可丢，
// 不值得为它拦住启动；但**写**失败必须上屏（见 saveIntent）。
func loadIntent() trayIntent {
	p, err := intentPath()
	if err != nil {
		return trayIntent{}
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return trayIntent{}
	}
	var t trayIntent
	_ = json.Unmarshal(raw, &t)
	return t
}

// saveIntent 0700 目录 + 0600 文件（用户私有目录，不该世界可读）。
// 失败必须**返回**给调用方上屏 —— 静默丢弃正是 D9 的原缺陷。
func saveIntent(t trayIntent) error {
	dir, err := intentDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "tray-intent.json"), append(b, '\n'), 0o600)
}
