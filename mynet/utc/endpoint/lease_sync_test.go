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

// P0 regression: 3-peer node (like n178) where utcd writes peer_transport= in
// [trust] across both -a and -b sides. Verifies resolveExportEndpoints covers
// all 3 pairs and ReconcileUTCConf recovers mode 0 -> 2 without silent drop.
func TestReconcileUTCConfMultiPeerTrustResolutionAndRecovery(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	utcConf := filepath.Join(root, "utc.conf")
	utcdConf := filepath.Join(root, "utcd.conf")

	const nowUnix uint64 = 1795500000
	const leaseExp uint64 = 1795586400
	m1 := strings.Repeat("1", 64)
	m2 := strings.Repeat("2", 64)
	m3 := strings.Repeat("3", 64)

	utcdBody := strings.Join([]string{
		"config_version=3",
		"state_dir=" + stateDir,
		"peer.p1.transport_id=8283289760467957",
		"peer.p1.udp_bind=127.0.0.1:19721",
		"peer.p2.transport_id=3764021915150630",
		"peer.p2.udp_bind=127.0.0.1:19710",
		"peer.p3.transport_id=9999999999999999",
		"peer.p3.udp_bind=127.0.0.1:19720",
	}, "\n") + "\n"
	if err := os.WriteFile(utcdConf, []byte(utcdBody), 0o600); err != nil {
		t.Fatal(err)
	}

	writeTrust := func(dirName string, pairID, localT, peerT uint64) {
		dir := filepath.Join(stateDir, "pairs", dirName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		trust := fmt.Sprintf("[trust]\nversion=1\nlocal_transport=%d\npeer_transport=%d\n[binding \"a-to-b\"]\npair_id=%d\ntarget_peer_id=%d\n",
			localT, peerT, pairID, peerT)
		if err := os.WriteFile(filepath.Join(dir, "trust"), []byte(trust), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Mix of -a and -b sides on the same multi-peer node
	writeTrust("3470419108809137-a", 3470419108809137, 3722810996882027, 8283289760467957)
	writeTrust("4470419108809138-b", 4470419108809138, 3722810996882027, 3764021915150630)
	writeTrust("5470419108809139-a", 5470419108809139, 3722810996882027, 9999999999999999)

	eps := resolveExportEndpoints(utcdConf, stateDir)
	if len(eps) != 3 {
		t.Fatalf("resolveExportEndpoints got %d entries (%v), want 3", len(eps), eps)
	}
	if eps[3470419108809137].Port != 19721 || eps[4470419108809138].Port != 19710 || eps[5470419108809139].Port != 19720 {
		t.Fatalf("unexpected resolved ports: %+v", eps)
	}

	// All 3 peers currently in mode=0 DIRECT after UTC was toggled off;
	// now pair 1 and pair 2 have bridge_up=1, pair 3 still bridge_up=0.
	initial := strings.Join([]string{
		"utc-v2 mynet-prod node178 3722810996882027 15 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 1001 3",
		"peer 3764021915150630 4470419108809138 8 0 1 1 0 0 00000000000000000000000000000000 0 0 " + m2,
		"peer 8283289760467957 3470419108809137 9 0 1 1 0 0 00000000000000000000000000000000 0 0 " + m1,
		"peer 9999999999999999 5470419108809139 13 0 1 1 0 0 00000000000000000000000000000000 0 0 " + m3,
	}, "\n") + "\n"
	if err := os.WriteFile(utcConf, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, p := range []struct {
		dir    string
		pairID uint64
		up     int
		man    string
	}{
		{"3470419108809137-a", 3470419108809137, 1, m1},
		{"4470419108809138-b", 4470419108809138, 1, m2},
		{"5470419108809139-a", 5470419108809139, 0, m3},
	} {
		dir := filepath.Join(stateDir, "pairs", p.dir)
		writeLeaseJSON(t, filepath.Join(dir, "lease-a-to-b.json"), p.pairID, 6, leaseExp, p.man)
		writeLeaseJSON(t, filepath.Join(dir, "lease-b-to-a.json"), p.pairID, 6, leaseExp, p.man)
		writeBridgeStatus(t, filepath.Join(dir, "bridge.status"), p.up, p.pairID, 6, leaseExp, nowUnix, 5555)
	}

	enabled := true
	updated, gen, err := ReconcileUTCConf(ReconcileOptions{
		StateDir:       stateDir,
		UTCConfPath:    utcConf,
		UTCDConfPath:   utcdConf,
		NowUnix:        nowUnix,
		UserEnabled:    &enabled,
		ProcessAliveFn: func(pid int) bool { return pid == 5555 },
	}, NewReconcileState())
	if err != nil {
		t.Fatalf("multi-peer reconcile error: %v", err)
	}
	if updated != 2 || gen != 16 {
		t.Fatalf("got updated=%d gen=%d, want 2 and 16", updated, gen)
	}
	got, _ := os.ReadFile(utcConf)
	body := string(got)
	if !strings.Contains(body, "peer 3764021915150630 4470419108809138 9 2 1 1 1795586400 4 00000000000000000000ffff7f000001 19710 0 "+m2) {
		t.Fatalf("pair 2 (-b side) did not recover to 19710:\n%s", body)
	}
	if !strings.Contains(body, "peer 8283289760467957 3470419108809137 10 2 1 1 1795586400 4 00000000000000000000ffff7f000001 19721 0 "+m1) {
		t.Fatalf("pair 1 (-a side) did not recover to 19721:\n%s", body)
	}
	if !strings.Contains(body, "peer 9999999999999999 5470419108809139 13 0 1 1 0 0 00000000000000000000000000000000 0 0 "+m3) {
		t.Fatalf("pair 3 (bridge_up=0) should stay mode=0:\n%s", body)
	}
}

// P1-a regression: when gnb restarts and writes a new utc.identity (new boot_id
// and pid), agent automatically rebinds the utc-v2 header, bumps config_gen and
// unlocks active EXPORT peers with a single pair_gen increment, then stays
// strictly idempotent on subsequent steady-state polls (matching prod n178/n182).
func TestReconcileUTCConfRebindsIdentityOnGNBRestart(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	utcConf := filepath.Join(root, "utc.conf")
	utcIdentity := filepath.Join(root, "utc.identity")

	const nowUnix uint64 = 1795500000
	const leaseExp uint64 = 1795586400
	m1 := strings.Repeat("a", 64)
	oldBoot := "11111111111111111111111111111111"
	newBoot := "22222222222222222222222222222222"

	// Existing prod-converged state (like n182: config_gen=14, pair_gen=11, mode=2)
	initial := strings.Join([]string{
		"utc-v2 mynet-prod node182 8283289760467957 14 " + oldBoot + " 15903 3",
		"peer 3722810996882027 3470419108809137 11 2 1 1 1795586400 4 00000000000000000000ffff7f000001 19711 0 " + m1,
	}, "\n") + "\n"
	if err := os.WriteFile(utcConf, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	pairDir := filepath.Join(stateDir, "pairs", "3470419108809137-b")
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-a-to-b.json"), 3470419108809137, 6, leaseExp, m1)
	writeLeaseJSON(t, filepath.Join(pairDir, "lease-b-to-a.json"), 3470419108809137, 6, leaseExp, m1)
	writeBridgeStatus(t, filepath.Join(pairDir, "bridge.status"), 1, 3470419108809137, 6, leaseExp, nowUnix, 20001)

	enabled := true
	state := NewReconcileState()

	// 1) Steady-state with matching utc.identity: strictly idempotent (no bump!)
	if err := os.WriteFile(utcIdentity, []byte("identity-v1 8283289760467957 "+oldBoot+" 15903\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := ReconcileOptions{
		StateDir:       stateDir,
		UTCConfPath:    utcConf,
		NowUnix:        nowUnix,
		UserEnabled:    &enabled,
		ProcessAliveFn: func(pid int) bool { return pid == 15903 || pid == 20001 || pid == 29999 },
	}
	updated, gen, err := ReconcileUTCConf(opts, state)
	if err != nil || updated != 0 || gen != 14 {
		t.Fatalf("steady-state must be idempotent: got updated=%d gen=%d err=%v", updated, gen, err)
	}

	// 2) gnb restarts with new boot_id & pid=29999
	if err := os.WriteFile(utcIdentity, []byte("identity-v1 8283289760467957 "+newBoot+" 29999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, gen, err = ReconcileUTCConf(opts, state)
	if err != nil || updated != 1 || gen != 15 {
		t.Fatalf("post-restart rebind got updated=%d gen=%d err=%v, want 1, 15, nil", updated, gen, err)
	}
	got, _ := os.ReadFile(utcConf)
	want := strings.Join([]string{
		"utc-v2 mynet-prod node182 8283289760467957 15 " + newBoot + " 29999 3",
		"peer 3722810996882027 3470419108809137 12 2 1 1 1795586400 4 00000000000000000000ffff7f000001 19711 0 " + m1,
	}, "\n") + "\n"
	if string(got) != want {
		t.Fatalf("rebound utc.conf mismatch:\ngot:\n%s\nwant:\n%s", string(got), want)
	}

	// 3) Next poll after rebind: strictly idempotent again (pair_gen stays 12, config_gen stays 15)
	updated, gen, err = ReconcileUTCConf(opts, state)
	if err != nil || updated != 0 || gen != 15 {
		t.Fatalf("post-rebind steady-state must be idempotent: got updated=%d gen=%d err=%v", updated, gen, err)
	}
}
