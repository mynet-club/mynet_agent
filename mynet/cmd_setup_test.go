package mynet

// setup v2：只写 profile/intent 并激活，**不生成密钥**。
// 密钥走 config keys set（导入）或 config keys generate（新建）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/mynet/keys"
	"github.com/mynet-club/mynet_agent/shared/config"
)

func setupInTempHome(t *testing.T, args ...string) int {
	t.Helper()
	t.Setenv("MYNET_HOME", t.TempDir())
	return cmdSetupV2(args)
}

func readKeysHex(t *testing.T, keysDir string) (priv, pub string) {
	t.Helper()
	pb, err := os.ReadFile(filepath.Join(keysDir, "node.private"))
	if err != nil {
		t.Fatal(err)
	}
	ub, err := os.ReadFile(filepath.Join(keysDir, "node.public"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(pb)), strings.TrimSpace(string(ub))
}

// setup 不生成密钥 —— 这是本文件最重要的不变量。
func TestSetupDoesNotGenerateKeys(t *testing.T) {
	if rc := setupInTempHome(t, "--local", "--node-id", "3001"); rc != 0 {
		t.Fatalf("setup rc=%d", rc)
	}
	root := os.Getenv("MYNET_HOME")
	keysDir := filepath.Join(root, "conf", "local", "nodes", "3001", "keys")
	if _, err := os.Stat(filepath.Join(keysDir, "node.private")); err == nil {
		t.Fatal("setup 不得生成 node.private —— 密钥必须 keys set / keys generate")
	}
	if _, err := os.Stat(filepath.Join(keysDir, "node.public")); err == nil {
		t.Fatal("setup 不得生成 node.public")
	}
}

func TestSetupLocalThenGenerateKeys(t *testing.T) {
	if rc := setupInTempHome(t, "--local", "--node-id", "3001",
		"--overlay", "10.0.0.1/32"); rc != 0 {
		t.Fatalf("setup --local 退出码 %d", rc)
	}
	// 显式生成（新装路径）
	if rc := configFaceKeysGenerate([]string{"--vpn", "gnb", "--node-id", "3001"}); rc != 0 {
		t.Fatalf("keys generate rc=%d", rc)
	}
	root := os.Getenv("MYNET_HOME")
	keysDir := filepath.Join(root, "conf", "local", "nodes", "3001", "keys")
	priv, pub := readKeysHex(t, keysDir)
	if len(priv) != 128 {
		t.Fatalf("gnb 私钥 %d hex，必须 128（CPI-61）", len(priv))
	}
	if len(pub) != 64 {
		t.Fatalf("公钥 %d hex，必须 64", len(pub))
	}
	// 不得再写 mynet.conf
	if _, err := os.Stat(filepath.Join(root, "conf", "mynet.conf")); err == nil {
		t.Fatal("不该再写 mynet.conf")
	}
	store := &config.Store{Root: root}
	m, err := store.LoadMode()
	if err != nil || m == nil || m.Mode != "local" {
		t.Fatalf("mode=%+v err=%v", m, err)
	}
	if _, err := store.LoadLocalIntent(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(keysDir, "node.private"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("private 权限 %v", st.Mode().Perm())
	}
}

// 导入路径：keys set 用已有私钥，setup 不碰密钥。
// 配对验证（spec）：公私钥不一致时**以私钥推导为准**，导入公钥被覆盖。
func TestSetupThenImportKeys(t *testing.T) {
	if rc := setupInTempHome(t, "--online", "--tenant", "t_9",
		"--node-id", "n2", "--vpn-type", "gnb"); rc != 0 {
		t.Fatalf("setup --online 退出码 %d", rc)
	}
	// 伪造一对 gnb hex（128/64）—— 刻意不配对，验证「以私钥为准」
	priv := strings.Repeat("ab", 64) // 128 hex
	pub := strings.Repeat("cd", 32)  // 64 hex（与 priv 不配对）
	if rc := configFaceKeysSet([]string{
		"--private", priv, "--public", pub,
		"--node-id", "n2", "--algorithm", "ed25519",
	}); rc != 0 {
		t.Fatalf("keys set rc=%d", rc)
	}
	root := os.Getenv("MYNET_HOME")
	gotPriv, gotPub := readKeysHex(t, filepath.Join(root, "conf", "online", "t_9", "nodes", "n2", "keys"))
	// 私钥原样落盘
	if gotPriv != priv {
		t.Fatalf("私钥不一致 priv=%s…", gotPriv[:8])
	}
	// 公钥应为**从私钥推导**的值（覆盖不配对的导入公钥）
	derived, derr := keys.DerivePublicKeyFromPrivateHex(priv, "ed25519")
	if derr != nil {
		t.Fatal(derr)
	}
	if gotPub != derived {
		t.Fatalf("公钥应以私钥推导为准: got %s… want %s…", gotPub[:8], derived[:8])
	}
	if gotPub == pub {
		t.Fatal("不配对的导入公钥不该被原样写入")
	}
	store := &config.Store{Root: root}
	p, err := store.LoadOnlineProfile("t_9")
	if err != nil {
		t.Fatal(err)
	}
	if p.NodeID != "n2" || p.Driver != "gnb" {
		t.Fatalf("%+v", p)
	}
	m, err := store.LoadMode()
	if err != nil || m.Mode != "online" || m.Active.TenantID != "t_9" {
		t.Fatalf("mode=%+v err=%v", m, err)
	}
}

// 只给 --private 也要能导入：公钥从私钥推导，meta.algorithm 落盘非空。
func TestKeysSetDerivesPublicFromPrivate(t *testing.T) {
	if rc := setupInTempHome(t, "--online", "--tenant", "t_9",
		"--node-id", "n2", "--vpn-type", "gnb"); rc != 0 {
		t.Fatalf("setup rc=%d", rc)
	}
	priv := strings.Repeat("ab", 64) // 128 hex，合法 gnb processed key
	if rc := configFaceKeysSet([]string{"--private", priv, "--node-id", "n2"}); rc != 0 {
		t.Fatalf("keys set（仅私钥）rc=%d", rc)
	}
	root := os.Getenv("MYNET_HOME")
	keysDir := filepath.Join(root, "conf", "online", "t_9", "nodes", "n2", "keys")
	gotPriv, gotPub := readKeysHex(t, keysDir)
	if gotPriv != priv {
		t.Fatal("私钥应原样落盘")
	}
	derived, err := keys.DerivePublicKeyFromPrivateHex(priv, "ed25519")
	if err != nil {
		t.Fatal(err)
	}
	if gotPub != derived {
		t.Fatalf("公钥应为私钥推导值: got %s… want %s…", gotPub[:8], derived[:8])
	}
	meta, err := keys.LoadNodeMeta(keysDir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Algorithm != "ed25519" {
		t.Fatalf("meta.algorithm=%q，推导路径不得留空", meta.Algorithm)
	}
}

// 私钥推导出错且未给公钥 → 拒绝写入（不落盘）。
func TestKeysSetRejectsUnparseablePrivate(t *testing.T) {
	if rc := setupInTempHome(t, "--online", "--tenant", "t_9",
		"--node-id", "n2", "--vpn-type", "gnb"); rc != 0 {
		t.Fatalf("setup rc=%d", rc)
	}
	bad := strings.Repeat("z", 128) // 128 字符但非 hex
	configFaceKeysSet([]string{"--private", bad, "--node-id", "n2"})
	root := os.Getenv("MYNET_HOME")
	keysDir := filepath.Join(root, "conf", "online", "t_9", "nodes", "n2", "keys")
	if _, err := os.Stat(filepath.Join(keysDir, "node.private")); err == nil {
		t.Fatal("推导失败的私钥不得落盘")
	}
}

func TestSetupOnlineWritesProfile(t *testing.T) {
	if rc := setupInTempHome(t, "--online", "--tenant", "t_9",
		"--node-id", "n2", "--vpn-type", "wireguard"); rc != 0 {
		t.Fatalf("setup --online 退出码 %d", rc)
	}
	root := os.Getenv("MYNET_HOME")
	// setup 不写密钥
	if _, err := os.Stat(filepath.Join(root, "conf", "online", "t_9", "nodes", "n2", "keys", "node.private")); err == nil {
		t.Fatal("setup 不得写私钥")
	}
	store := &config.Store{Root: root}
	p, err := store.LoadOnlineProfile("t_9")
	if err != nil {
		t.Fatal(err)
	}
	if p.NodeID != "n2" || p.Driver != "wireguard" {
		t.Fatalf("%+v", p)
	}
	m, err := store.LoadMode()
	if err != nil || m.Mode != "online" || m.Active.TenantID != "t_9" {
		t.Fatalf("mode=%+v err=%v", m, err)
	}
}

func TestSetupRequiresModeAndNode(t *testing.T) {
	if rc := setupInTempHome(t, "--node-id", "1"); rc == 0 {
		t.Fatal("缺 --local/--online 应失败")
	}
	if rc := setupInTempHome(t, "--local"); rc == 0 {
		t.Fatal("缺 --node-id 应失败")
	}
	if rc := setupInTempHome(t, "--online", "--node-id", "1"); rc == 0 {
		t.Fatal("--online 缺 --tenant 应失败")
	}
}

// 防御：`--help` 这类标志位不得被当成 node-id 写进配置（实测踩过）。
func TestNodeClaimRejectsFlagAsID(t *testing.T) {
	for _, bad := range []string{"--help", "-h", "--vpn-type", ""} {
		args := []string{bad}
		if bad == "" {
			args = nil
		}
		if rc := nodeClaim(args); rc != 2 {
			t.Errorf("nodeClaim(%q) = %d, want 2", bad, rc)
		}
	}
}
