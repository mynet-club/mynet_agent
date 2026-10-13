package mynet

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// C1-S1 回归：`config keys set` 私钥走 stdin（--private-stdin，对齐 auth login --password-stdin），
// 不走 argv —— argv 会进 `ps` 与本机 API 的 argv 回显面。
func TestParseKeysSetArgsStdin(t *testing.T) {
	secret := strings.Repeat("ab", 64)
	priv, pub, nodeID, algo, err := parseKeysSetArgs(
		[]string{"--private-stdin", "--public", "aabb", "--node-id", "n1", "--algorithm", "ed25519"},
		strings.NewReader(secret+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if priv != secret {
		t.Fatalf("stdin 私钥没读到: %q", priv)
	}
	if pub != "aabb" || nodeID != "n1" || algo != "ed25519" {
		t.Fatalf("其余参数解析错: pub=%q nodeID=%q algo=%q", pub, nodeID, algo)
	}
}

// --private HEX 保留给人工终端一次性导入（安装文档命令）。
func TestParseKeysSetArgsDirectFlag(t *testing.T) {
	secret := strings.Repeat("cd", 64)
	priv, _, _, _, err := parseKeysSetArgs([]string{"--private", secret}, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if priv != secret {
		t.Fatalf("--private 没读到: %q", priv)
	}
}

// 既无 --private 也无 --private-stdin → 明确报错（提示走 stdin）。
func TestParseKeysSetArgsMissingPrivate(t *testing.T) {
	_, _, _, _, err := parseKeysSetArgs([]string{"--node-id", "n1"}, strings.NewReader(""))
	if err == nil {
		t.Fatal("缺私钥应报错")
	}
	if !strings.Contains(err.Error(), "--private-stdin") {
		t.Fatalf("报错应提示 --private-stdin: %v", err)
	}
}

// --private-stdin 但 stdin 空 → 报错，不写空私钥。
func TestParseKeysSetArgsEmptyStdin(t *testing.T) {
	_, _, _, _, err := parseKeysSetArgs([]string{"--private-stdin"}, strings.NewReader("\n"))
	if err == nil {
		t.Fatal("空 stdin 应报错")
	}
}

// D4-E 裁定⑤：`mynet config keys` 跟 mode.json 的**实际 form** + 正确 home 树。
// keys_dir 必须是节点唯一存放位 conf/<form>/<nodeId>/；home/home_source 必须报
// MYNET_HOME 的真实落点 —— 不许退化成 exe 推断的幻影路径（183 实测 /opt/mynet/…）。
func TestConfigKeysShowFollowsModeFormAndHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)

	// local 档
	if rc := cmdSetupV2([]string{"--local", "--node-id", "3001"}); rc != 0 {
		t.Fatalf("setup rc=%d", rc)
	}
	if rc := configFaceKeysGenerate([]string{"--vpn", "gnb", "--node-id", "3001"}); rc != 0 {
		t.Fatalf("keys generate rc=%d", rc)
	}
	out := captureStdout(t, configFaceKeysShow)
	for _, want := range []string{
		`"keys_dir": "` + filepath.Join(root, "conf", "local", "3001") + `"`,
		`"home": "` + root + `"`,
		`"home_source": "env"`,
		`"mode": "local"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("keys show 缺 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"warning"`) {
		t.Fatalf("MYNET_HOME 显式设定时不许告警:\n%s", out)
	}

	// online 档：form 跟 mode.json（tenant 维度收进 mode.json，无租户层）
	if rc := cmdSetupV2([]string{"--online", "--tenant", "t_9", "--node-id", "n7", "--vpn-type", "gnb"}); rc != 0 {
		t.Fatalf("setup --online rc=%d", rc)
	}
	out = captureStdout(t, configFaceKeysShow)
	for _, want := range []string{
		`"keys_dir": "` + filepath.Join(root, "conf", "online", "n7") + `"`,
		`"home": "` + root + `"`,
		`"mode": "online"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("online keys show 缺 %q:\n%s", want, out)
		}
	}
}

// captureStdout 跑 f 并收回它写到 os.Stdout 的全部输出。
func captureStdout(t *testing.T, f func() int) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := f()
	_ = w.Close()
	os.Stdout = old
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("rc=%d\n%s", code, b)
	}
	return string(b)
}
