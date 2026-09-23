package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", dir)
	t.Setenv("MYNET_CONFIG_HOME", filepath.Join(dir, "conf"))

	cfg := &Config{Email: "user@example.com", Token: "tok-abc", ConfigVersion: 1}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != cfg.Email || got.Token != cfg.Token {
		t.Fatalf("got %+v", got)
	}
	if got.UI == nil {
		t.Fatal("UI should be backfilled")
	}
	if !Exists() {
		t.Fatal("Exists")
	}
	// 权限：配置文件不得可被其他用户读
	fi, err := os.Stat(filepath.Join(dir, "conf", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config perm too open: %v", fi.Mode().Perm())
	}
}

func TestClearTokensKeepsLastEmail(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", dir)
	t.Setenv("MYNET_CONFIG_HOME", filepath.Join(dir, "conf"))
	if err := Save(&Config{Email: "a@b.c", Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	if err := ClearTokens(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "" || got.Email != "" {
		t.Fatalf("tokens not cleared: %+v", got)
	}
	if got.LastEmail != "a@b.c" {
		t.Fatalf("LastEmail=%q", got.LastEmail)
	}
}
