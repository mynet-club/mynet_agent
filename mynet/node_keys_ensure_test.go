package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/mynet/keys"
)

func TestEnsureNodeKeysPreservesIdentity(t *testing.T) {
	for _, vpn := range []string{"gnb", "wireguard"} {
		t.Run(vpn, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "keys")
			if err := ensureNodeKeys(dir, vpn, "42", ""); err != nil {
				t.Fatal(err)
			}
			before, _, _, _, err := keys.LoadNodeKeys(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := ensureNodeKeys(dir, vpn, "42", ""); err != nil {
				t.Fatal(err)
			}
			after, _, _, _, _ := keys.LoadNodeKeys(dir)
			if before != after {
				t.Fatal("retry rotated identity")
			}
			if err := ensureNodeKeys(dir, vpn, "43", ""); err == nil {
				t.Fatal("accepted other identity")
			}
		})
	}
}
func TestEnsureNodeKeysRefusesPartialIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.private")
	os.WriteFile(path, []byte("incomplete"), 0600)
	if err := ensureNodeKeys(dir, "gnb", "42", ""); err == nil {
		t.Fatal("overwrote partial import")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "incomplete" {
		t.Fatal("private key modified")
	}
}
func TestCloudNodeVPN(t *testing.T) {
	rows := []any{map[string]any{"node_id": float64(42), "vpn_type": []any{"wireguard"}}}
	vpn, err := cloudNodeVPN(rows, "42")
	if err != nil || vpn != "wireguard" {
		t.Fatalf("%s %v", vpn, err)
	}
	if _, err := cloudNodeVPN(rows, "43"); err == nil {
		t.Fatal("accepted missing device")
	}
}

// 雪花 ID 经 encoding/json 落成 float64，fmt.Sprint 会打成科学计数法
// （1.056…e+15），与 "1056391690228289" 永远对不上。按整数比较。
func TestCloudNodeVPNSnowflakeID(t *testing.T) {
	const snowflake = "1056391690228289"
	rows := []any{
		map[string]any{"node_id": float64(1056391690228289), "vpn_type": "gnb"},
		map[string]any{"node_id": snowflake, "vpn_type": "gnb"},
		map[string]any{"node_id": int64(1056391690228289), "vpn_type": "gnb"},
	}
	for _, row := range rows {
		m := row.(map[string]any)
		r := []any{m}
		vpn, err := cloudNodeVPN(r, snowflake)
		if err != nil || vpn != "gnb" {
			t.Fatalf("node_id=%v -> %q %v", m["node_id"], vpn, err)
		}
	}
}

func TestNodeIDEquals(t *testing.T) {
	const id = "1056391690228289"
	cases := []struct {
		v    any
		want bool
	}{
		{float64(1056391690228289), true},
		{id, true},
		{int64(1056391690228289), true},
		{float64(42), false},
		{"42", false},
		{nil, false},
	}
	for _, c := range cases {
		if got := nodeIDEquals(c.v, id); got != c.want {
			t.Fatalf("nodeIDEquals(%v,%s)=%v want %v", c.v, id, got, c.want)
		}
	}
}

func TestEnsureNodeKeysRefusesMismatchedPair(t *testing.T) {
	// gnb：security/<id>.public 被改坏（与私钥不配对）→ 拒绝、私钥不许被转
	dir := t.TempDir()
	if err := ensureNodeKeys(dir, "gnb", "42", ""); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "security", "42.private"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "security", "42.public"), []byte("bad-public"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureNodeKeys(dir, "gnb", "42", ""); err == nil {
		t.Fatal("accepted corrupt public key")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "security", "42.private"))
	if string(before) != string(after) {
		t.Fatal("rotated private key")
	}

	// wireguard：wg.conf 私钥行损坏 → 视为部分导入，拒绝自动重建（不覆盖）
	wdir := t.TempDir()
	if err := ensureNodeKeys(wdir, "wireguard", "42", ""); err != nil {
		t.Fatal(err)
	}
	wgBefore, err := os.ReadFile(filepath.Join(wdir, "wg.conf"))
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(wgBefore), "PrivateKey = ", "PrivateKey = !!!", 1)
	if err := os.WriteFile(filepath.Join(wdir, "wg.conf"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureNodeKeys(wdir, "wireguard", "42", ""); err == nil {
		t.Fatal("accepted corrupt wg.conf")
	}
	wgAfter, _ := os.ReadFile(filepath.Join(wdir, "wg.conf"))
	if string(wgBefore) == string(wgAfter) {
		t.Fatal("损坏的 wg.conf 不该被悄悄重建")
	}

	// 驱动不匹配：有效 wireguard 身份 + gnb 请求 → 拒绝
	cdir := t.TempDir()
	if err := ensureNodeKeys(cdir, "wireguard", "42", ""); err != nil {
		t.Fatal(err)
	}
	if err := ensureNodeKeys(cdir, "gnb", "42", ""); err == nil {
		t.Fatal("accepted wrong driver")
	}
}
