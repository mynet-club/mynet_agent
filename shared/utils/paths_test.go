package utils

// 配置树 v2 路径：三分布局、租户/插件目录名防穿越。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigTreeLayout(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", dir)

	if ConfDir() != filepath.Join(dir, "conf") {
		t.Fatalf("ConfDir=%s", ConfDir())
	}
	if LocalConfDir() != filepath.Join(dir, "conf", "local") {
		t.Fatalf("LocalConfDir=%s", LocalConfDir())
	}
	td, err := OnlineTenantDir("t_1")
	if err != nil {
		t.Fatal(err)
	}
	if td != filepath.Join(dir, "conf", "online", "t_1") {
		t.Fatalf("OnlineTenantDir=%s", td)
	}
	pc := PluginsConfDir(LocalConfDir())
	if filepath.Base(pc) != "plugins" {
		t.Fatalf("PluginsConfDir=%s", pc)
	}
	if PluginsRoot() != filepath.Join(dir, "plugins") {
		t.Fatalf("PluginsRoot=%s", PluginsRoot())
	}
	pv, err := PluginVarDir("proxy")
	if err != nil {
		t.Fatal(err)
	}
	if pv != filepath.Join(dir, "var", "plugins", "proxy") {
		t.Fatalf("PluginVarDir=%s", pv)
	}
	if GnbConfDir() != filepath.Join(dir, "driver", "gnb", "conf") {
		t.Fatalf("GnbConfDir=%s", GnbConfDir())
	}
	gn, err := GnbNodeConfDir("8107")
	if err != nil {
		t.Fatal(err)
	}
	if gn != filepath.Join(dir, "driver", "gnb", "conf", "8107") {
		t.Fatalf("GnbNodeConfDir=%s", gn)
	}
	if ModeFilePath() != filepath.Join(dir, "mode.json") {
		t.Fatalf("ModeFilePath=%s", ModeFilePath())
	}
	if !strings.Contains(LastGoodDir(), filepath.Join("state", "last-good")) {
		t.Fatalf("LastGoodDir=%s", LastGoodDir())
	}
}

func TestOnlineTenantDirRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", dir)
	for _, bad := range []string{"", "..", "a/b", `a\b`, "."} {
		if _, err := OnlineTenantDir(bad); err == nil {
			t.Errorf("应拒 %q", bad)
		}
	}
}

func TestValidateDirName(t *testing.T) {
	if err := ValidateDirName("proxy"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDirName("t_1"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "a\\b", "a:b"} {
		if err := ValidateDirName(bad); err == nil {
			t.Errorf("应拒 %q", bad)
		}
	}
}

func TestNoLegacyMynetHome(t *testing.T) {
	// 不设 MYNET_HOME 时也不得落进 ~/.mynet
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MYNET_HOME", filepath.Join(t.TempDir(), "mh"))
	_ = ConfDir()
	if _, err := os.Stat(filepath.Join(home, ".mynet")); err == nil {
		t.Fatal("不应创建 legacy ~/.mynet")
	}
}

func TestConfDirVarDirStable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", dir)
	if ConfDir() == "" || VarDir() == "" {
		t.Fatal("ConfDir/VarDir 应返回有效路径")
	}
	if !filepath.IsAbs(ConfDir()) || !filepath.IsAbs(VarDir()) {
		t.Fatal("应基于绝对 HOME")
	}
}
