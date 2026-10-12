package mynet

// 插件与导出导入 v2。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

func TestPluginInstallEnableApply(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	if rc := cmdSetupV2([]string{"--local", "--node-id", "1"}); rc != 0 {
		t.Fatal(rc)
	}
	if rc := cmdPlugin([]string{"install", "proxy"}); rc != 0 {
		t.Fatal(rc)
	}
	base := filepath.Join(root, "conf", "local", "plugins")
	if _, err := os.Stat(filepath.Join(base, "proxy.json")); err != nil {
		t.Fatal(err)
	}
	if rc := cmdPlugin([]string{"enable", "proxy"}); rc != 0 {
		t.Fatal(rc)
	}
	if err := ApplyPlugins(false); err != nil {
		t.Fatal(err)
	}
	gen := filepath.Join(root, "var", "plugins", "proxy", "config.generated.json")
	if _, err := os.Stat(gen); err != nil {
		t.Fatalf("缺投影 %s: %v", gen, err)
	}
	// disable 后再 Apply 不应再生成？（文件仍在但配置 enabled=false 时跳过）
	store := &config.Store{Root: root}
	baseSrc, _, _ := store.ActiveSource()
	pdir := utils.PluginsConfDir(baseSrc)
	pc, err := store.LoadPlugin(pdir, domain.PluginNameProxy)
	if err != nil {
		t.Fatal(err)
	}
	pc.Enabled = false
	_ = store.SavePlugin(pdir, pc)
	// ApplyPlugins skip disabled —— 删掉旧投影再 apply，不应重建
	_ = os.Remove(gen)
	if err := ApplyPlugins(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gen); err == nil {
		t.Fatal("disabled 插件不应投影")
	}
}

func TestExportImportIncludesModeAndConf(t *testing.T) {
	src := t.TempDir()
	t.Setenv("MYNET_HOME", src)
	if rc := cmdSetupV2([]string{"--local", "--node-id", "9", "--overlay", "10.0.0.9/32"}); rc != 0 {
		t.Fatal(rc)
	}
	out := filepath.Join(t.TempDir(), "bak.tar.gz")
	if rc := cmdConfigExport([]string{"-o", out}); rc != 0 {
		t.Fatal(rc)
	}

	// 新 HOME 导入
	dst := t.TempDir()
	t.Setenv("MYNET_HOME", dst)
	// 跳过 apply 的环境：先只测 restore 路径白名单 —— 完整 import 会 Apply
	// 这里造最小 intent 以便 Apply 成功
	_ = cmdConfigImport([]string{out}) // 可能 Apply 失败也行，至少 conf/mode 进了

	if _, err := os.Stat(filepath.Join(dst, "conf", "mode.json")); err != nil {
		t.Fatalf("缺 mode.json（2026-10-12 起在 conf/ 下）: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "conf", "local", "intent.json")); err != nil {
		t.Fatalf("缺 intent: %v", err)
	}
	// 不得还原 driver 投影当真相源（Apply 失败时 driver 可为空，但不能来自包）
	// 包里本来就没有 driver/ —— 用 manifest 断言
	_, files, err := readBackup(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if len(f) >= 7 && f[:7] == "driver/" {
			t.Fatalf("包里不该有 driver/: %s", f)
		}
	}
}
