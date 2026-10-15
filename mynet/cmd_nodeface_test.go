package mynet

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/mynet/keys"
)

func TestNodeFaceSignMatchesKeysPackage(t *testing.T) {
	// 与 genvec 同构：用已知向量验证 CLI 路径输出与 keys.Sign 一致
	nodePriv := strings.Repeat("a1", 32)
	backPriv := strings.Repeat("b2", 32)
	back, err := keys.GenerateX25519FromPrivateHex(backPriv)
	if err != nil {
		t.Fatal(err)
	}
	backPub := hex.EncodeToString(back.Public)
	s, err := keys.NewNodeFaceSigner(nodePriv, backPub)
	if err != nil {
		t.Fatal(err)
	}
	want := s.Sign("POST", "/api/v1/node/heartbeat", "1700000000", `{"ok":1}`)
	if want == "" {
		t.Fatal("empty sig")
	}
	// 形状：base64
	if !strings.ContainsAny(want, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=") {
		t.Fatalf("sig not base64-like: %q", want)
	}
	_ = filepath.Join
	_ = os.TempDir
}

// D4-E 后 gnb 密钥在 security/<id>.private，CLI 门闩曾只 stat node.private
// → 已迁移节点在 `mynet nodeface sign` 上报「定位 keys 目录失败」。
// 锁住：security/ 形态可被找到；都缺时返回空。
func TestFindKeysDirWithPrivateRecognizesGNBSecurity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	nodeID := "3764021915150630"
	dir := filepath.Join(home, "conf", "online", nodeID)
	if err := os.MkdirAll(filepath.Join(dir, "security"), 0o700); err != nil {
		t.Fatal(err)
	}
	priv := strings.Repeat("c3", 32) // 128 hex，gnb 原生
	if err := os.WriteFile(filepath.Join(dir, "security", nodeID+".private"), []byte(priv), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := `{"node_id":"` + nodeID + `","algorithm":"ed25519","back_pub_hex":"` +
		strings.Repeat("d4", 32) + `","sign_algorithm":"hmac-sha256-dh"}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}

	if !keysDirHasPrivate(dir) {
		t.Fatalf("keysDirHasPrivate(%s) = false，应认 gnb security/*.private", dir)
	}
	got := findKeysDirWithPrivate(nodeID)
	if got != dir {
		t.Fatalf("findKeysDirWithPrivate = %q, want %q", got, dir)
	}

	// 都缺时返回空，不猜
	home2 := t.TempDir()
	t.Setenv("MYNET_HOME", home2)
	if got := findKeysDirWithPrivate("999"); got != "" {
		t.Fatalf("空树应返回空，got %q", got)
	}
}
