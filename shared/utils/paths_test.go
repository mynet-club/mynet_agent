package utils

// 配置树 v2 路径：三分布局、租户/插件目录名防穿越。

import (
	"errors"
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
	if ModeFilePath() != filepath.Join(dir, "conf", "mode.json") {
		t.Fatalf("ModeFilePath=%s（mode.json 在 conf/ 下）", ModeFilePath())
	}
	if GatewaysFile() != filepath.Join(dir, "conf", "gateways.json") {
		t.Fatalf("GatewaysFile=%s", GatewaysFile())
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

// 密钥是节点身份：切到 online 档后仍应找到 conf/local 下的既有密钥（跨档不漂）。
func TestResolveKeysDirCrossModeFallback(t *testing.T) {
	home := t.TempDir()
	local := filepath.Join(home, "conf", "local")
	if err := os.MkdirAll(filepath.Join(local, "keys"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "keys", "node.private"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	online := filepath.Join(home, "conf", "online", "t_1")
	if err := os.MkdirAll(online, 0o700); err != nil {
		t.Fatal(err)
	}
	got := ResolveKeysDir(online, "8283289760467957")
	want := filepath.Join(local, "keys")
	if got != want {
		t.Fatalf("online 档应回退到 conf/local/keys，得 %q 期望 %q", got, want)
	}
	// local 档直接命中
	if g := ResolveKeysDir(local, "8283289760467957"); g != want {
		t.Fatalf("local 档应命中 conf/local/keys，得 %q", g)
	}
}

// HomeSource 三态判据（C1-S3：原判据两分支同返回 "exe"，"cwd" 永远打不出）。
// 与 GetMyNetHome 解析链一一对应：env > exe > cwd（"."）。
func TestHomeSourceThreeStates(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		exePath string
		exeErr  error
		want    string
	}{
		{"env 优先", "/var/lib/mynet", "/x/bin/mynet", nil, "env"},
		{"exe 可取（bin 上推布局）", "", "/x/bin/mynet", nil, "exe"},
		{"exe 可取（exeDir 直用）", "", "/x/mynet", nil, "exe"},
		{"exe 取不到回退 cwd", "", "", errors.New("no executable"), "cwd"},
	}
	for _, c := range cases {
		if got := homeSourceFrom(c.env, c.exePath, c.exeErr); got != c.want {
			t.Errorf("%s: homeSourceFrom()=%q，want %q", c.name, got, c.want)
		}
	}
}

// HomeSource 集成：设了 MYNET_HOME 必报 env（无 env 的分支依赖进程环境，
// 只锁 env 路径这一确定态）。
func TestHomeSourceEnv(t *testing.T) {
	t.Setenv(envHome, "/tmp/x")
	if got := HomeSource(); got != "env" {
		t.Fatalf("HomeSource()=%q，want env", got)
	}
}
