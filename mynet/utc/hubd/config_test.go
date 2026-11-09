package hubd

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func validConfigJSON(t *testing.T) []byte {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	text := `{
		"address": "127.0.0.1:19620",
		"scope": "scope.test",
		"transport_id": "transport-a",
		"account_id": "acct-a",
		"issuer_public_key": "` + hex.EncodeToString(key.Public().(ed25519.PublicKey)) + `",
		"identity": {
			"instance": "instance-a", "local_id": 10, "peer_id": 20,
			"boot_id": "` + testBoot + `", "pid": 123,
			"capabilities": 3, "config_gen": 5
		},
		"pair_id": 30, "side": 0, "mode": 1,
		"conf_dir": "/tmp/gnb", "summary_path": "/tmp/state/utc.hub.json"
	}`
	return []byte(text)
}

func TestLoadConfigAcceptsMinimalWiring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.json")
	if err := os.WriteFile(path, validConfigJSON(t), 0644); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Scope != testScope || config.Identity.Scope != testScope ||
		config.RenewLead == 0 || config.LeaseTTL == 0 ||
		config.StatusPath != "/tmp/gnb/utc.status" ||
		config.VerifiedPath != "/tmp/state/utc.hub.json.verified" {
		t.Fatalf("config defaults = %+v", config)
	}
}

func TestLoadConfigAllowsDeferredIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.json")
	identity := []byte(`"boot_id": "` + testBoot + `", "pid": 123,`)
	deferred := []byte(`"boot_id": "", "pid": 0,`)
	if err := os.WriteFile(path, bytes.Replace(validConfigJSON(t), identity, deferred, 1), 0644); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Identity.BootID != "" || config.Identity.PID != 0 {
		t.Fatalf("deferred identity = %+v", config.Identity)
	}
	malformed := []byte(`"boot_id": "not-a-boot-id", "pid": 123,`)
	if err := os.WriteFile(path, bytes.Replace(validConfigJSON(t), identity, malformed, 1), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("malformed boot_id was accepted")
	}
}

func TestLoadConfigRejectsUnknownFieldAndWritableGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.json")
	body := append(validConfigJSON(t), []byte("\n")...)
	body = bytes.Replace(body, []byte("\"pair_id\""), []byte("\"evil\": 1, \"pair_id\""), 1)
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unknown config field was accepted")
	}
	if err := os.WriteFile(path, validConfigJSON(t), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("group/world-writable config was accepted")
	}
}
