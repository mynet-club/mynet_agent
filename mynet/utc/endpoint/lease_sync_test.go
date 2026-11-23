package endpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLeaseJSON(t *testing.T, path string, pairID, pairGen, expiresAt uint64, manifest string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(
		`{"version":1,"pair_id":%d,"pair_gen":%d,"expires_at":%d,"manifest":"%s"}`,
		pairID, pairGen, expiresAt, manifest,
	)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSyncUTCConfFromStateDirAdvancesPairGenAndPreservesOtherPeers(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	utcConf := filepath.Join(root, "utc.conf")

	oldManifest := strings.Repeat("a", 64)
	newManifest := strings.Repeat("b", 64)
	otherManifest := strings.Repeat("c", 64)

	otherPeerLine := "peer 9999999999999999 5708783663303928 5 1 1795474245 4 7f000001000000000000000000000000 19002 0 " + otherManifest
	initial := strings.Join([]string{
		"utc-v1 mynet-prod node182 8283289760467957 10",
		"peer 3722810996882027 3470419108809137 5 1 1795474245 4 7f000001000000000000000000000000 19001 0 " + oldManifest,
		otherPeerLine,
	}, "\n") + "\n"
	if err := os.WriteFile(utcConf, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	// utcd installed pair_gen=6 with a-to-b expiring slightly earlier than b-to-a
	pairDir := filepath.Join(stateDir, "pairs", "3470419108809137-a")
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-a-to-b.json"), 3470419108809137, 6, 1795560645, newManifest)
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-b-to-a.json"), 3470419108809137, 6, 1795560700, newManifest)

	updated, newGen, err := SyncUTCConfFromStateDir(stateDir, utcConf)
	if err != nil {
		t.Fatalf("SyncUTCConfFromStateDir error: %v", err)
	}
	if updated != 1 || newGen != 11 {
		t.Fatalf("got updated=%d newGen=%d, want updated=1 newGen=11", updated, newGen)
	}

	gotBytes, err := os.ReadFile(utcConf)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"utc-v1 mynet-prod node182 8283289760467957 11",
		"peer 3722810996882027 3470419108809137 6 1 1795560645 4 7f000001000000000000000000000000 19001 0 " + newManifest,
		otherPeerLine,
	}, "\n") + "\n"
	if string(gotBytes) != want {
		t.Fatalf("utc.conf mismatch:\ngot:\n%s\nwant:\n%s", string(gotBytes), want)
	}

	// Idempotent second pass: no write, config_gen unchanged
	updated, newGen, err = SyncUTCConfFromStateDir(stateDir, utcConf)
	if err != nil {
		t.Fatalf("second sync error: %v", err)
	}
	if updated != 0 || newGen != 11 {
		t.Fatalf("second sync got updated=%d newGen=%d, want 0 and 11", updated, newGen)
	}

	// Older lease must never rewind pair_gen
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-a-to-b.json"), 3470419108809137, 4, 1795400000, oldManifest)
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-b-to-a.json"), 3470419108809137, 4, 1795400000, oldManifest)
	updated, newGen, err = SyncUTCConfFromStateDir(stateDir, utcConf)
	if err != nil {
		t.Fatalf("rewind check error: %v", err)
	}
	if updated != 0 || newGen != 11 {
		t.Fatalf("older lease rewound utc.conf: updated=%d newGen=%d", updated, newGen)
	}
}

func TestResolveBinaryPrefersEnvAndFallback(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "utcd")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MYNET_UTCD_BIN", fakeBin)
	if got := ResolveBinary(filepath.Join(dir, "missing")); got != fakeBin {
		t.Fatalf("ResolveBinary with MYNET_UTCD_BIN = %q, want %q", got, fakeBin)
	}

	t.Setenv("MYNET_UTCD_BIN", "")
	if got := ResolveBinary(dir); got != fakeBin {
		t.Fatalf("ResolveBinary with prefixBinDir = %q, want %q", got, fakeBin)
	}
}
