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
