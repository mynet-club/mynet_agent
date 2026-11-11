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
			"instance": "instance-a", "local_id": 10,
			"boot_id": "` + testBoot + `", "pid": 123,
			"capabilities": 3, "config_gen": 5
		},
		"pairs": [
			{"pair_id": 30, "peer_id": 20, "side": 0, "mode": 1,
			 "summary_path": "/tmp/state/utc.hub.json"}
		],
		"conf_dir": "/tmp/gnb"
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
		len(config.Pairs) != 1 || config.Pairs[0].PairID != testPairID ||
		config.Pairs[0].PeerID != testPeerID ||
		config.Pairs[0].VerifiedPath != "/tmp/state/utc.hub.json.verified" {
		t.Fatalf("config defaults = %+v", config)
	}
}

func TestLoadConfigAcceptsADPairWithLocalEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.json")
	body := bytes.Replace(validConfigJSON(t),
		[]byte(`"mode": 1,
			 "summary_path": "/tmp/state/utc.hub.json"`),
		[]byte(`"mode": 2, "local_address": "127.0.0.1", "local_port": 14001,
			 "summary_path": "/tmp/state/utc.hub.json"`), 1)
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := config.Pairs[0].LocalEndpoint
	if config.Pairs[0].Mode != 2 || endpoint == nil ||
		endpoint.Address != "127.0.0.1" || endpoint.Port != 14001 {
		t.Fatalf("A-D pair = %+v", config.Pairs[0])
	}
}

func TestLoadConfigValidatesMeterPath(t *testing.T) {
	directory := t.TempDir()
	body := bytes.Replace(validConfigJSON(t), []byte(`"conf_dir": "/tmp/gnb"`),
		[]byte(`"conf_dir": "/tmp/gnb", "meter_path": "`+
			filepath.Join(directory, "meter.txt")+`"`), 1)
	path := filepath.Join(t.TempDir(), "hub.json")
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.MeterPath != filepath.Join(directory, "meter.txt") {
		t.Fatalf("meter path = %q", config.MeterPath)
	}
	for _, bad := range []string{"relative.txt",
		filepath.Join(directory, "missing", "meter.txt")} {
		invalid := bytes.Replace(validConfigJSON(t),
			[]byte(`"conf_dir": "/tmp/gnb"`),
			[]byte(`"conf_dir": "/tmp/gnb", "meter_path": "`+bad+`"`), 1)
		if err := os.WriteFile(path, invalid, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("meter_path %q must be rejected", bad)
		}
	}
}

func TestLoadConfigRejectsInvalidPairWiring(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	base := func(pairs string) []byte {
		return []byte(`{
			"address": "127.0.0.1:19620",
			"scope": "scope.test",
			"transport_id": "transport-a",
			"account_id": "acct-a",
			"issuer_public_key": "` + hex.EncodeToString(key.Public().(ed25519.PublicKey)) + `",
			"identity": {"instance": "instance-a", "local_id": 10,
				"boot_id": "` + testBoot + `", "pid": 123,
				"capabilities": 3, "config_gen": 5},
			"pairs": [` + pairs + `],
			"conf_dir": "/tmp/gnb"
		}`)
	}
	cases := map[string]string{
		"no pairs":             ``,
		"A-D without endpoint": `{"pair_id": 30, "peer_id": 20, "side": 0, "mode": 2, "summary_path": "/tmp/a"}`,
		"A-S with endpoint": `{"pair_id": 30, "peer_id": 20, "side": 0, "mode": 1,
			"local_address": "127.0.0.1", "local_port": 14001, "summary_path": "/tmp/a"}`,
		"duplicate pair id": `{"pair_id": 30, "peer_id": 20, "side": 0, "mode": 1, "summary_path": "/tmp/a"},
			{"pair_id": 30, "peer_id": 21, "side": 0, "mode": 1, "summary_path": "/tmp/b"}`,
		"duplicate peer id": `{"pair_id": 30, "peer_id": 20, "side": 0, "mode": 1, "summary_path": "/tmp/a"},
			{"pair_id": 31, "peer_id": 20, "side": 0, "mode": 1, "summary_path": "/tmp/b"}`,
		"unknown mode": `{"pair_id": 30, "peer_id": 20, "side": 0, "mode": 3, "summary_path": "/tmp/a"}`,
	}
	for name, pairs := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hub.json")
			if err := os.WriteFile(path, base(pairs), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("accepted invalid pair wiring: %s", name)
			}
		})
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
