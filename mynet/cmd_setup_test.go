package mynet

// setup v2：本地 / 联网两套落盘，身份进 keys/，不再写 mynet.conf。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent-V2/shared/config"
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

func TestSetupLocalGnb128Hex(t *testing.T) {
	if rc := setupInTempHome(t, "--local", "--node-id", "3001",
		"--overlay", "10.0.0.1/32"); rc != 0 {
		t.Fatalf("setup --local 退出码 %d", rc)
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
	// mode.json 激活 local
	store := &config.Store{Root: root}
	m, err := store.LoadMode()
	if err != nil || m == nil || m.Mode != "local" {
		t.Fatalf("mode=%+v err=%v", m, err)
	}
	// intent 存在
	if _, err := store.LoadLocalIntent(); err != nil {
		t.Fatal(err)
	}
	// 权限
	st, err := os.Stat(filepath.Join(keysDir, "node.private"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("private 权限 %v", st.Mode().Perm())
	}
}

func TestSetupOnlineWritesProfile(t *testing.T) {
	if rc := setupInTempHome(t, "--online", "--tenant", "t_9",
		"--node-id", "n2", "--vpn-type", "wireguard"); rc != 0 {
		t.Fatalf("setup --online 退出码 %d", rc)
	}
	root := os.Getenv("MYNET_HOME")
	priv, _ := readKeysHex(t, filepath.Join(root, "conf", "online", "t_9", "nodes", "n2", "keys"))
	if len(priv) != 64 {
		t.Fatalf("wg 私钥 %d hex，必须 64", len(priv))
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
