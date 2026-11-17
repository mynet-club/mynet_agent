package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/tunnelstate"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// restore 只按意图拉起；「已在跑」问执行器（runningFn），mynetd 不做 pid 判活。
func TestRestoreTunnelIfNeededByIntent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	// auto_restore 默认开（无 agent.json）

	// 无意图 → 不拉
	var started []string
	restoreTunnelIfNeeded(func(string) bool { return false }, func(d string) error {
		started = append(started, d)
		return nil
	})
	if len(started) != 0 {
		t.Fatalf("无意图不该拉起，got %v", started)
	}

	// 意图 true + 未在跑 → 拉起
	if err := tunnelstate.MarkWant(true, "gnb"); err != nil {
		t.Fatal(err)
	}
	restoreTunnelIfNeeded(func(string) bool { return false }, func(d string) error {
		started = append(started, d)
		return nil
	})
	if len(started) != 1 || started[0] != "gnb" {
		t.Fatalf("started=%v", started)
	}

	// 意图 true + 已在跑 → 跳过
	started = nil
	restoreTunnelIfNeeded(func(string) bool { return true }, func(d string) error {
		started = append(started, d)
		return nil
	})
	if len(started) != 0 {
		t.Fatalf("已在跑不该重复拉起，got %v", started)
	}

	// 开关关掉 → 不拉
	if err := os.MkdirAll(filepath.Join(home, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "conf", "agent.json"), []byte(`{"auto_restore_tunnel":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	started = nil
	restoreTunnelIfNeeded(func(string) bool { return false }, func(d string) error {
		started = append(started, d)
		return nil
	})
	if len(started) != 0 {
		t.Fatalf("开关关闭不该拉起，got %v", started)
	}
	_ = utils.VarDir() // 路径同源自检（无副作用）
}
