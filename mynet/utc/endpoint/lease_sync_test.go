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

func writeBridgeStatus(t *testing.T, path string, bridgeUp int, pairID, pairGen, expiresAt, updatedAt uint64, pid int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(
		"bridge_up=%d\npair_id=%d\npair_gen=%d\nexpires_at=%d\nupdated_at=%d\npid=%d\n",
		bridgeUp, pairID, pairGen, expiresAt, updatedAt, pid,
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

func TestSyncUTCConfFromStateDirSupportsV2Format(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	utcConf := filepath.Join(root, "utc.conf")

	manifest := strings.Repeat("1", 64)
	initial := strings.Join([]string{
		"utc-v2 mynet-prod nodeHome 8283289760467957 6 92bdba3eb158a4a10f86df189a8bb38e 15903 3",
		"peer 3722810996882027 3470419108809137 5 2 1 1 1795474245 4 00000000000000000000ffff7f000001 19711 0 " + manifest,
	}, "\n") + "\n"
	if err := os.WriteFile(utcConf, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	pairDir := filepath.Join(stateDir, "pairs", "3470419108809137-b")
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-a-to-b.json"), 3470419108809137, 6, 1795560645, manifest)
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-b-to-a.json"), 3470419108809137, 6, 1795560645, manifest)

	updated, newGen, err := SyncUTCConfFromStateDir(stateDir, utcConf)
	if err != nil {
		t.Fatalf("v2 sync error: %v", err)
	}
	if updated != 1 || newGen != 7 {
		t.Fatalf("got updated=%d newGen=%d, want 1 and 7", updated, newGen)
	}

	gotBytes, err := os.ReadFile(utcConf)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"utc-v2 mynet-prod nodeHome 8283289760467957 7 92bdba3eb158a4a10f86df189a8bb38e 15903 3",
		"peer 3722810996882027 3470419108809137 6 2 1 1 1795560645 4 00000000000000000000ffff7f000001 19711 0 " + manifest,
	}, "\n") + "\n"
	if string(gotBytes) != want {
		t.Fatalf("utc-v2 mismatch:\ngot:\n%s\nwant:\n%s", string(gotBytes), want)
	}
}

func TestResolveUTCDConfigPathCanonicalAndLegacyFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_UTC_CONFIG", "")

	legacy := filepath.Join(home, "utc", "utcd.conf")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("config_version=3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveUTCDConfigPath(home); got != legacy {
		t.Fatalf("legacy fallback = %q, want %q", got, legacy)
	}

	canonical := filepath.Join(home, "conf", "utc", "utcd.conf")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte("config_version=3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveUTCDConfigPath(home); got != canonical {
		t.Fatalf("canonical path = %q, want %q", got, canonical)
	}
}

func TestReconcileUTCConfFailOpenDebounceAndRecoveryV2(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	utcConf := filepath.Join(root, "utc.conf")
	utcdConf := filepath.Join(root, "utcd.conf")

	const pairID uint64 = 3470419108809137
	const nowUnix uint64 = 1795500000
	const leaseExp uint64 = 1795586400
	manifest := strings.Repeat("1", 64)

	utcdBody := strings.Join([]string{
		"config_version=3",
		"state_dir=" + stateDir,
		"peer.n178.pair_id=3470419108809137",
		"peer.n178.udp_bind=127.0.0.1:19711",
	}, "\n") + "\n"
	if err := os.WriteFile(utcdConf, []byte(utcdBody), 0o600); err != nil {
		t.Fatal(err)
	}

	initial := strings.Join([]string{
		"utc-v2 mynet-prod nodeHome 8283289760467957 6 92bdba3eb158a4a10f86df189a8bb38e 15903 3",
		"peer 3722810996882027 3470419108809137 5 2 1 1 1795586400 4 00000000000000000000ffff7f000001 19711 0 " + manifest,
	}, "\n") + "\n"
	if err := os.WriteFile(utcConf, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	pairDir := filepath.Join(stateDir, "pairs", "3470419108809137-b")
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-a-to-b.json"), pairID, 5, leaseExp, manifest)
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-b-to-a.json"), pairID, 5, leaseExp, manifest)
	writeBridgeStatus(t, filepath.Join(pairDir, "bridge.status"), 0, pairID, 5, leaseExp, nowUnix, 12345)

	enabled := true
	state := NewReconcileState()
	opts := ReconcileOptions{
		StateDir:          stateDir,
		UTCConfPath:       utcConf,
		UTCDConfPath:      utcdConf,
		NowUnix:           nowUnix,
		UserEnabled:       &enabled,
		DownDebounceTicks: 2,
		ProcessAliveFn:    func(pid int) bool { return pid == 12345 },
	}

	// Tick 1 with bridge_up=0: held by debounce (no flap on transient reconnect)
	updated, gen, err := ReconcileUTCConf(opts, state)
	if err != nil {
		t.Fatalf("tick 1 error: %v", err)
	}
	if updated != 0 || gen != 6 {
		t.Fatalf("tick 1 should debounce: got updated=%d gen=%d", updated, gen)
	}

	// Tick 2 with bridge_up=0: falls back to mode=0 DIRECT UDP, bumping pair_gen 5->6 and config_gen 6->7
	updated, gen, err = ReconcileUTCConf(opts, state)
	if err != nil {
		t.Fatalf("tick 2 error: %v", err)
	}
	if updated != 1 || gen != 7 {
		t.Fatalf("tick 2 should fall back: got updated=%d gen=%d", updated, gen)
	}
	gotBytes, _ := os.ReadFile(utcConf)
	wantDirect := strings.Join([]string{
		"utc-v2 mynet-prod nodeHome 8283289760467957 7 92bdba3eb158a4a10f86df189a8bb38e 15903 3",
		"peer 3722810996882027 3470419108809137 6 0 1 1 0 0 00000000000000000000000000000000 0 0 " + manifest,
	}, "\n") + "\n"
	if string(gotBytes) != wantDirect {
		t.Fatalf("direct fallback mismatch:\ngot:\n%s\nwant:\n%s", string(gotBytes), wantDirect)
	}

	// Bridge comes UP (bridge_up=1): immediately switches back to mode=2 EXPORT,
	// bumping pair_gen 6->7 and config_gen 7->8 and restoring 127.0.0.1:19711.
	writeBridgeStatus(t, filepath.Join(pairDir, "bridge.status"), 1, pairID, 5, leaseExp, nowUnix+5, 12345)
	updated, gen, err = ReconcileUTCConf(opts, state)
	if err != nil {
		t.Fatalf("recovery error: %v", err)
	}
	if updated != 1 || gen != 8 {
		t.Fatalf("recovery got updated=%d gen=%d, want 1 and 8", updated, gen)
	}
	gotBytes, _ = os.ReadFile(utcConf)
	wantExport := strings.Join([]string{
		"utc-v2 mynet-prod nodeHome 8283289760467957 8 92bdba3eb158a4a10f86df189a8bb38e 15903 3",
		"peer 3722810996882027 3470419108809137 7 2 1 1 1795586400 4 00000000000000000000ffff7f000001 19711 0 " + manifest,
	}, "\n") + "\n"
	if string(gotBytes) != wantExport {
		t.Fatalf("export recovery mismatch:\ngot:\n%s\nwant:\n%s", string(gotBytes), wantExport)
	}

	// User disables UTC: immediate fallback (no debounce) even when bridge_up=1
	disabled := false
	opts.UserEnabled = &disabled
	updated, gen, err = ReconcileUTCConf(opts, state)
	if err != nil {
		t.Fatalf("user disable error: %v", err)
	}
	if updated != 1 || gen != 9 {
		t.Fatalf("user disable got updated=%d gen=%d, want 1 and 9", updated, gen)
	}
	gotBytes, _ = os.ReadFile(utcConf)
	wantDisabled := strings.Join([]string{
		"utc-v2 mynet-prod nodeHome 8283289760467957 9 92bdba3eb158a4a10f86df189a8bb38e 15903 3",
		"peer 3722810996882027 3470419108809137 8 0 1 1 0 0 00000000000000000000000000000000 0 0 " + manifest,
	}, "\n") + "\n"
	if string(gotBytes) != wantDisabled {
		t.Fatalf("user disable mismatch:\ngot:\n%s\nwant:\n%s", string(gotBytes), wantDisabled)
	}
}

func TestReconcileUTCConfStartsDirectAndActivatesFromUTCDConf(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	utcConf := filepath.Join(root, "utc.conf")
	utcdConf := filepath.Join(root, "utcd.conf")

	const pairID uint64 = 3470419108809137
	const nowUnix uint64 = 1795500000
	const leaseExp uint64 = 1795586400
	manifest := strings.Repeat("a", 64)

	// v3 utcd.conf without explicit pair_id (resolved via trust file / single peer)
	utcdBody := strings.Join([]string{
		"config_version=3",
		"state_dir=" + stateDir,
		"peer.n178.transport_id=3722810996882027",
		"peer.n178.udp_bind=127.0.0.1:19711",
	}, "\n") + "\n"
	if err := os.WriteFile(utcdConf, []byte(utcdBody), 0o600); err != nil {
		t.Fatal(err)
	}

	// GNB starts in mode=0 DIRECT UDP (never cached an export endpoint before)
	initial := strings.Join([]string{
		"utc-v2 mynet-prod nodeMac 3764021915150630 1 27f37d2a17b4ec9489e32e1ade1d4a39 43498 3",
		"peer 3722810996882027 3470419108809137 1 0 1 1 0 0 00000000000000000000000000000000 0 0 " + manifest,
	}, "\n") + "\n"
	if err := os.WriteFile(utcConf, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	pairDir := filepath.Join(stateDir, "pairs", "3470419108809137-a")
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-a-to-b.json"), pairID, 6, leaseExp, manifest)
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-b-to-a.json"), pairID, 6, leaseExp, manifest)
	writeBridgeStatus(t, filepath.Join(pairDir, "bridge.status"), 1, pairID, 6, leaseExp, nowUnix, 9999)

	enabled := true
	state := NewReconcileState()
	updated, gen, err := ReconcileUTCConf(ReconcileOptions{
		StateDir:       stateDir,
		UTCConfPath:    utcConf,
		UTCDConfPath:   utcdConf,
		NowUnix:        nowUnix,
		UserEnabled:    &enabled,
		ProcessAliveFn: func(pid int) bool { return pid == 9999 },
	}, state)
	if err != nil {
		t.Fatalf("activate error: %v", err)
	}
	if updated != 1 || gen != 2 {
		t.Fatalf("activate got updated=%d gen=%d, want 1 and 2", updated, gen)
	}
	gotBytes, _ := os.ReadFile(utcConf)
	want := strings.Join([]string{
		"utc-v2 mynet-prod nodeMac 3764021915150630 2 27f37d2a17b4ec9489e32e1ade1d4a39 43498 3",
		"peer 3722810996882027 3470419108809137 6 2 1 1 1795586400 4 00000000000000000000ffff7f000001 19711 0 " + manifest,
	}, "\n") + "\n"
	if string(gotBytes) != want {
		t.Fatalf("activate mismatch:\ngot:\n%s\nwant:\n%s", string(gotBytes), want)
	}
}

func TestReconcileUTCConfDeadPIDTriggersFallback(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	utcConf := filepath.Join(root, "utc.conf")

	const pairID uint64 = 3470419108809137
	const nowUnix uint64 = 1795500000
	const leaseExp uint64 = 1795586400
	manifest := strings.Repeat("2", 64)

	initial := strings.Join([]string{
		"utc-v2 mynet-prod nodeHome 8283289760467957 3 92bdba3eb158a4a10f86df189a8bb38e 15903 3",
		"peer 3722810996882027 3470419108809137 5 2 1 1 1795586400 4 00000000000000000000ffff7f000001 19711 0 " + manifest,
	}, "\n") + "\n"
	if err := os.WriteFile(utcConf, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	pairDir := filepath.Join(stateDir, "pairs", "3470419108809137-b")
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-a-to-b.json"), pairID, 5, leaseExp, manifest)
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-b-to-a.json"), pairID, 5, leaseExp, manifest)
	// Stale bridge_up=1 from a crashed utcd PID
	writeBridgeStatus(t, filepath.Join(pairDir, "bridge.status"), 1, pairID, 5, leaseExp, nowUnix, 77777)

	enabled := true
	state := NewReconcileState()
	opts := ReconcileOptions{
		StateDir:          stateDir,
		UTCConfPath:       utcConf,
		NowUnix:           nowUnix,
		UserEnabled:       &enabled,
		DownDebounceTicks: 1,
		ProcessAliveFn:    func(pid int) bool { return false },
	}
	updated, gen, err := ReconcileUTCConf(opts, state)
	if err != nil {
		t.Fatalf("dead pid error: %v", err)
	}
	if updated != 1 || gen != 4 {
		t.Fatalf("dead pid got updated=%d gen=%d, want 1 and 4", updated, gen)
	}
	gotBytes, _ := os.ReadFile(utcConf)
	if !strings.Contains(string(gotBytes), "peer 3722810996882027 3470419108809137 6 0 1 1 0 0 00000000000000000000000000000000 0 0 ") {
		t.Fatalf("expected fallback to mode=0 DIRECT, got:\n%s", string(gotBytes))
	}
}
