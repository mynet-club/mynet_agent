package mynet

// migrate-layout 测试（D4-E）：dry-run 不动盘 / 密钥 sha256 同代核对搬移 /
// 旧树 .bak-<ts> 保留不删 / 密钥冲突该节点失败其余继续 / wg hex→base64 只在搬运边界转。

import (
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/mynet/keys"
)

const (
	migPrivHex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 128 hex（gnb ed25519 seed）
	migPubHex = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // 64 hex
)

// seedGnbTree 造旧 gnb 投影树 driver/gnb/conf/<id>/。
func seedGnbTree(t *testing.T, home, node string) string {
	t.Helper()
	dir := filepath.Join(home, "driver", "gnb", "conf", node)
	sec := filepath.Join(dir, "security")
	if err := os.MkdirAll(sec, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string, perm os.FileMode) {
		if err := os.WriteFile(p, []byte(s), perm); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "node.conf"), "nodeid "+node+"\n", 0o644)
	write(filepath.Join(dir, "route.conf"), "route "+node+"\n", 0o644)
	write(filepath.Join(sec, node+".private"), migPrivHex, 0o600)
	write(filepath.Join(sec, node+".public"), migPubHex, 0o644)
	return dir
}

// seedKeysTree 造旧 keys 树 conf/<form>/nodes/<id>/keys/（form=local|online/<tenant>）。
func seedKeysTree(t *testing.T, home, space, node, privHex, algo string) string {
	t.Helper()
	dir := filepath.Join(home, "conf", filepath.FromSlash(space), "nodes", node, "keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string, perm os.FileMode) {
		if err := os.WriteFile(p, []byte(s), perm); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "node.private"), privHex+"\n", 0o600)
	write(filepath.Join(dir, "node.public"), migPubHex+"\n", 0o644)
	write(filepath.Join(dir, "meta.json"),
		`{"node_id":"`+node+`","algorithm":"`+algo+`","back_pub_hex":"cc","sign_algorithm":"hmac-sha256-dh"}`+"\n", 0o600)
	return dir
}

func TestMigrateLayoutDryRunLeavesDiskUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	gnbDir := seedGnbTree(t, home, "9001")
	keysDir := seedKeysTree(t, home, "local", "9001", migPrivHex, "ed25519")

	if rc := cmdConfigMigrateLayout([]string{"--dry-run"}); rc != 0 {
		t.Fatalf("dry-run rc=%d", rc)
	}
	// 目标不得建、旧树不得改名
	if _, err := os.Stat(filepath.Join(home, "conf", "local", "9001", "node.conf")); err == nil {
		t.Fatal("dry-run 不该搬文件")
	}
	for _, d := range []string{gnbDir, keysDir} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Fatalf("dry-run 不该动旧树 %s: %v", d, err)
		}
	}
}

func TestMigrateLayoutMovesGnbTreeAndKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	gnbDir := seedGnbTree(t, home, "9001")
	keysDir := seedKeysTree(t, home, "local", "9001", migPrivHex, "ed25519")

	if rc := cmdConfigMigrateLayout(nil); rc != 0 {
		t.Fatalf("migrate rc=%d", rc)
	}
	// 目标：conf/local/9001（无 mode.json → form=local），密钥与驱动 conf 同居
	tgt := filepath.Join(home, "conf", "local", "9001")
	for _, f := range []string{"node.conf", "route.conf", filepath.Join("security", "9001.private"), filepath.Join("security", "9001.public")} {
		if _, err := os.Stat(filepath.Join(tgt, f)); err != nil {
			t.Fatalf("缺 %s: %v", f, err)
		}
	}
	// 同代搬移：私钥字节级一致（sha256 核对在命令内做，这里复核结果）
	got, err := os.ReadFile(filepath.Join(tgt, "security", "9001.private"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != migPrivHex {
		t.Fatalf("私钥搬移不一致: %q", got)
	}
	// meta.json 随迁（back_pub_hex 不许丢）
	mb, err := os.ReadFile(filepath.Join(tgt, "meta.json"))
	if err != nil {
		t.Fatalf("meta.json 未随迁: %v", err)
	}
	if !strings.Contains(string(mb), `"back_pub_hex":"cc"`) && !strings.Contains(string(mb), `"back_pub_hex": "cc"`) {
		t.Fatalf("meta.json 丢字段: %s", mb)
	}
	// 旧树保留 .bak-<ts>，不删
	for _, d := range []string{gnbDir, keysDir} {
		found := false
		parent := filepath.Dir(d)
		base := filepath.Base(d)
		ents, _ := os.ReadDir(parent)
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), base+".bak-") {
				found = true
			}
		}
		if !found {
			t.Fatalf("旧树 %s 应改名 .bak-<ts> 保留", d)
		}
	}
}

func TestMigrateLayoutKeyConflictFailsNodeOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	// 9001：gnb 树与 keys 树同代（应成功）；9002：两处私钥不同代（应失败）
	seedGnbTree(t, home, "9001")
	seedKeysTree(t, home, "local", "9001", migPrivHex, "ed25519")
	gnb2 := seedGnbTree(t, home, "9002")
	other := strings.Repeat("ab", 64) // 128 hex，与 migPrivHex 不同
	seedKeysTree(t, home, "local", "9002", other, "ed25519")

	rc := cmdConfigMigrateLayout(nil)
	if rc == 0 {
		t.Fatal("有节点密钥冲突时应返回非 0")
	}
	// 9001 照常迁
	if _, err := os.Stat(filepath.Join(home, "conf", "local", "9001", "security", "9001.private")); err != nil {
		t.Fatalf("9001 应照常迁移: %v", err)
	}
	// 9002 不迁、旧树原样保留（不改名）
	if _, err := os.Stat(filepath.Join(home, "conf", "local", "9002", "security", "9002.private")); err == nil {
		t.Fatal("9002 密钥冲突不该迁")
	}
	if st, err := os.Stat(gnb2); err != nil || !st.IsDir() {
		t.Fatalf("9002 旧树应原样保留: %v", err)
	}
}

func TestMigrateLayoutWGKeysToNativeConf(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	// 旧 wg 投影树 + x25519 keys 树（hex）→ 目标 wg.conf（base64 44）
	wgDir := filepath.Join(home, "driver", "wireguard", "9003")
	if err := os.MkdirAll(wgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wgDir, "wg.conf"),
		[]byte("[Interface]\nAddress = 10.9.0.1/24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wgPrivHex := strings.Repeat("cd", 32) // 64 hex = 32 字节
	seedKeysTree(t, home, "local", "9003", wgPrivHex, "x25519")

	if rc := cmdConfigMigrateLayout(nil); rc != 0 {
		t.Fatalf("migrate rc=%d", rc)
	}
	tgt := filepath.Join(home, "conf", "local", "9003")
	got, err := keys.ReadWGPrivateKeyHex(tgt)
	if err != nil {
		t.Fatalf("目标 wg.conf 无 PrivateKey: %v", err)
	}
	if got != wgPrivHex {
		t.Fatalf("hex↔base64 搬运后回读不一致: got %s want %s", got, wgPrivHex)
	}
	// 磁盘只存驱动原生格式：wg.conf 里是 base64 44，不是 hex
	raw, err := os.ReadFile(filepath.Join(tgt, "wg.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), wgPrivHex) {
		t.Fatalf("wg.conf 不该存 hex 副本: %s", raw)
	}
	b64 := base64.StdEncoding.EncodeToString(mustHex(t, wgPrivHex))
	if !strings.Contains(string(raw), "PrivateKey = "+b64) {
		t.Fatalf("wg.conf 应存 base64 44 私钥: %s", raw)
	}
	// wg-tree 的既有内容（Address 等）随树搬入
	if !strings.Contains(string(raw), "Address = 10.9.0.1/24") {
		t.Fatalf("wg-tree 内容应随迁: %s", raw)
	}
	// 旧树保留
	if _, err := os.Stat(wgDir); err == nil {
		t.Fatal("旧 wg 树应改名 .bak-<ts>")
	}
}

func TestMigrateLayoutIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	seedGnbTree(t, home, "9001")
	seedKeysTree(t, home, "local", "9001", migPrivHex, "ed25519")

	if rc := cmdConfigMigrateLayout(nil); rc != 0 {
		t.Fatalf("first rc=%d", rc)
	}
	// 第二次：旧树已 .bak，无待迁移
	if rc := cmdConfigMigrateLayout(nil); rc != 0 {
		t.Fatalf("second rc=%d", rc)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
