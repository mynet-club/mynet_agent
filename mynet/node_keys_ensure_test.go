package mynet

import (
	"github.com/mynet-club/mynet_agent/mynet/keys"
	"os"
	"path/filepath"
	"testing"
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

func TestEnsureNodeKeysRefusesMismatchedPair(t *testing.T) {
	dir := t.TempDir()
	if err := ensureNodeKeys(dir, "wireguard", "42", ""); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "node.private"))
	os.WriteFile(filepath.Join(dir, "node.public"), []byte("bad-public"), 0644)
	if err := ensureNodeKeys(dir, "wireguard", "42", ""); err == nil {
		t.Fatal("accepted corrupt public key")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "node.private"))
	if string(before) != string(after) {
		t.Fatal("rotated private key")
	}
	if err := ensureNodeKeys(dir, "gnb", "42", ""); err == nil {
		t.Fatal("accepted wrong driver")
	}
}
