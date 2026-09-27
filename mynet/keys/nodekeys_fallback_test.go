package keys

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadNodeKeysAcceptsGnbSecurityNames(t *testing.T) {
	dir := t.TempDir()
	// gnb security 布局：<nodeid>.private / .public
	mustWrite := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("8107260975133902.private", "aabb")
	mustWrite("8107260975133902.public", "ccdd")
	mustWrite("meta.json", `{"node_id":"8107260975133902","algorithm":"ed25519"}`)

	priv, pub, algo, nid, err := LoadNodeKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if priv != "aabb" || pub != "ccdd" || algo != "ed25519" || nid != "8107260975133902" {
		t.Fatalf("got priv=%q pub=%q algo=%q nid=%q", priv, pub, algo, nid)
	}
}

func TestLoadNodeKeysPrefersNodePrivate(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "node.private"), []byte("11\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "node.public"), []byte("22\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "9001.private"), []byte("xx\n"), 0o600)
	priv, pub, _, _, err := LoadNodeKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if priv != "11" || pub != "22" {
		t.Fatalf("got %q %q", priv, pub)
	}
}
