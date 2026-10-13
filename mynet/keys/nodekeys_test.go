package keys

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateNodeKeysFormats(t *testing.T) {
	priv, pub, algo, err := GenerateNodeKeys("gnb")
	if err != nil {
		t.Fatal(err)
	}
	if algo != "ed25519" {
		t.Fatalf("gnb algo=%q want ed25519", algo)
	}
	if len(priv) != 128 || len(pub) != 64 {
		t.Fatalf("gnb hex lens priv=%d pub=%d want 128/64", len(priv), len(pub))
	}

	priv, pub, algo, err = GenerateNodeKeys("wireguard")
	if err != nil {
		t.Fatal(err)
	}
	if algo != "x25519" {
		t.Fatalf("wg algo=%q want x25519", algo)
	}
	if len(priv) != 64 || len(pub) != 64 {
		t.Fatalf("wg hex lens priv=%d pub=%d want 64/64", len(priv), len(pub))
	}

	if _, _, _, err := GenerateNodeKeys("openvpn"); err == nil {
		t.Fatal("未知 vpn-type 应报错")
	}
}

// 落盘 → 回读：字段、权限、meta 一次对齐。
func TestWriteLoadNodeKeysRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	priv, pub, algo, err := GenerateNodeKeys("gnb")
	if err != nil {
		t.Fatal(err)
	}
	const (
		nodeID = "n42"
		backPk = "bb11bb11bb11bb11bb11bb11bb11bb11bb11bb11bb11bb11bb11bb11bb11bb11" // 64 hex
	)
	if err := WriteNodeKeys(dir, priv, pub, algo, nodeID, backPk); err != nil {
		t.Fatal(err)
	}

	// 权限：private/meta 0600，public 0644
	for name, want := range map[string]os.FileMode{
		"node.private": 0o600,
		"node.public":  0o644,
		"meta.json":    0o600,
	} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Fatalf("%s perm=%o want %o", name, got, want)
		}
	}

	gotPriv, gotPub, gotAlgo, gotNode, err := LoadNodeKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gotPriv != priv || gotPub != pub || gotAlgo != algo || gotNode != nodeID {
		t.Fatalf("roundtrip mismatch\n priv %q/%q\n pub %q/%q\n algo %q/%q\n node %q/%q",
			gotPriv, priv, gotPub, pub, gotAlgo, algo, gotNode, nodeID)
	}

	meta, err := LoadNodeMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.BackPubHex != backPk {
		t.Fatalf("back_pub=%q want %q", meta.BackPubHex, backPk)
	}
	if meta.SignAlgorithm != "hmac-sha256-dh" {
		t.Fatalf("sign_algorithm=%q", meta.SignAlgorithm)
	}
	if meta.NodeID != nodeID || meta.Algorithm != algo {
		t.Fatalf("meta node/algo = %q/%q", meta.NodeID, meta.Algorithm)
	}
}

// 旧 setup 手写 meta.json 仍能读（极简 `"key": "value"` 布局）。
func TestLoadNodeMetaLegacyFormat(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node.private"), []byte("aa\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node.public"), []byte("bb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := "{\n  \"node_id\": \"legacy1\",\n  \"algorithm\": \"ed25519\",\n  \"back_pub_hex\": \"cc\",\n  \"sign_algorithm\": \"hmac-sha256-dh\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	meta, err := LoadNodeMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.NodeID != "legacy1" || meta.Algorithm != "ed25519" || meta.BackPubHex != "cc" {
		t.Fatalf("legacy meta parsed wrong: %+v", meta)
	}

	priv, pub, algo, node, err := LoadNodeKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if priv != "aa" || pub != "bb" || algo != "ed25519" || node != "legacy1" {
		t.Fatalf("legacy keys: %q %q %q %q", priv, pub, algo, node)
	}
}

// meta.json 缺失时 keys 仍可读，algorithm/nodeID 留空。
func TestLoadNodeKeysWithoutMeta(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node.private"), []byte("dd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node.public"), []byte("ee\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	priv, pub, algo, node, err := LoadNodeKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if priv != "dd" || pub != "ee" {
		t.Fatalf("keys %q %q", priv, pub)
	}
	if algo != "" || node != "" {
		t.Fatalf("无 meta 时 algo/node 应空，得 %q %q", algo, node)
	}
	if _, err := LoadNodeMeta(dir); err == nil {
		t.Fatal("缺 meta.json 应报错")
	}
}

func TestLoadNodeKeysMissing(t *testing.T) {
	if _, _, _, _, err := LoadNodeKeys(t.TempDir()); err == nil {
		t.Fatal("缺 node.private 应报错")
	}
}

// 写回再写回：覆盖旧文件不留残渣，且 JSON 合法。
func TestWriteNodeKeysOverwrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	p1, u1, a1, err := GenerateNodeKeys("wireguard")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteNodeKeys(dir, p1, u1, a1, "n1", "b1"); err != nil {
		t.Fatal(err)
	}
	p2, u2, a2, err := GenerateNodeKeys("gnb")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteNodeKeys(dir, p2, u2, a2, "n2", "b2"); err != nil {
		t.Fatal(err)
	}

	priv, pub, algo, node, err := LoadNodeKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if priv != p2 || pub != u2 || algo != a2 || node != "n2" {
		t.Fatalf("覆盖后应为第二次写入，得 %q %q %q %q", priv, pub, algo, node)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m NodeKeyMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("meta.json 非法: %v\n%s", err, raw)
	}
	if m.BackPubHex != "b2" {
		t.Fatalf("后端=%q", m.BackPubHex)
	}
	if strings.Contains(string(raw), "b1") {
		t.Fatalf("旧值残留: %s", raw)
	}
}

// 断链回归：早期 keys set/generate 把 back_pub_hex 写死空串，
// mynetd 节点面因此永远装配不了。回填必须容错（meta 缺/目录缺）且不动私钥面。
func TestUpdateNodeMetaBackPub(t *testing.T) {
	dir := t.TempDir()
	if err := WriteNodeKeys(dir, "aa", "bb", "ed25519", "n1", ""); err != nil {
		t.Fatal(err)
	}
	// 空值 = no-op，不报错也不清已有值
	if err := UpdateNodeMetaBackPub(dir, "n1", ""); err != nil {
		t.Fatal(err)
	}
	if err := UpdateNodeMetaBackPub(dir, "n1", "cc"); err != nil {
		t.Fatal(err)
	}
	m, err := LoadNodeMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.BackPubHex != "cc" || m.NodeID != "n1" || m.SignAlgorithm != "hmac-sha256-dh" {
		t.Fatalf("meta 回填结果不对: %+v", m)
	}
	// 私钥面不许被碰
	if b, _ := os.ReadFile(filepath.Join(dir, "node.private")); string(b) != "aa\n" {
		t.Fatalf("node.private 被动过: %q", b)
	}
	// meta 不存在：按已知信息重建（node claim 先于 keys 写入的场景）
	dir2 := filepath.Join(t.TempDir(), "keys")
	if err := UpdateNodeMetaBackPub(dir2, "n2", "dd"); err != nil {
		t.Fatal(err)
	}
	m2, err := LoadNodeMeta(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if m2.NodeID != "n2" || m2.BackPubHex != "dd" {
		t.Fatalf("重建 meta 不对: %+v", m2)
	}
}
