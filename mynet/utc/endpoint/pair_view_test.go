package endpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testManifest = "1111111111111111111111111111111111111111111111111111111111111111"

func writeProjection(t *testing.T, rows ...string) string {
	t.Helper()
	header := "utc-v2 mynet-prod nodeTest 1000 7 00112233445566778899aabbccddeeff 12345 3"
	body := header + "\n"
	for _, row := range rows {
		body += row + "\n"
	}
	path := filepath.Join(t.TempDir(), "utc.conf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePairState(t *testing.T, pairID, pairGen, expiresAt uint64) string {
	t.Helper()
	stateDir := t.TempDir()
	pairDir := filepath.Join(stateDir, "pairs", fmt.Sprintf("%d-b", pairID))
	if err := os.MkdirAll(pairDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lease := fmt.Sprintf(`{"pair_id":%d,"pair_gen":%d,"expires_at":%d,"manifest":"%s"}`,
		pairID, pairGen, expiresAt, testManifest)
	for _, name := range []string{"lease-a-to-b.json", "lease-b-to-a.json"} {
		if err := os.WriteFile(filepath.Join(pairDir, name), []byte(lease), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bridge := fmt.Sprintf("bridge_up=1\npair_id=%d\npair_gen=%d\nexpires_at=%d\nupdated_at=1800000000\npid=%d\n",
		pairID, pairGen, expiresAt, os.Getpid())
	if err := os.WriteFile(filepath.Join(pairDir, "bridge.status"), []byte(bridge), 0o600); err != nil {
		t.Fatal(err)
	}
	return stateDir
}

func adRow(peerID, pairID uint64) string {
	return fmt.Sprintf("peer %d %d 5 2 1 1 1900000000 4 00000000000000000000ffff7f000001 19710 0 %s",
		peerID, pairID, testManifest)
}

func directRow(peerID, pairID uint64) string {
	return fmt.Sprintf("peer %d %d 5 0 1 1 0 0 00000000000000000000000000000000 0 0 %s",
		peerID, pairID, testManifest)
}

func TestPairViewTCPWhenBridgeAndLeaseHealthy(t *testing.T) {
	home := t.TempDir()
	stateDir := writePairState(t, 3000, 5, 1900000000)
	conf := writeProjection(t, adRow(2000, 3000))
	store, _ := LoadPairIntents(home)
	if _, err := store.Set(2000, true, 100); err != nil {
		t.Fatal(err)
	}

	views, err := BuildPairViews(stateDir, conf, home, 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("want 1 view, got %d", len(views))
	}
	v := views[0]
	if v.State != PairStateTCP || v.IntentState != "active" || !v.BridgeUp || !v.LeaseValid || v.Mode != 2 {
		t.Fatalf("unexpected tcp view: %+v", v)
	}
}

func TestPairViewPendingAndRejectedWithoutProjection(t *testing.T) {
	home := t.TempDir()
	stateDir := writePairState(t, 3000, 5, 1900000000)
	conf := writeProjection(t, adRow(2000, 3000))
	store, _ := LoadPairIntents(home)
	if _, err := store.Set(2001, true, 100); err != nil {
		t.Fatal(err)
	}

	views, err := BuildPairViews(stateDir, conf, home, 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	var pending *PairView
	for i := range views {
		if views[i].PeerID == 2001 {
			pending = &views[i]
		}
	}
	if pending == nil || pending.State != PairStatePending || pending.Reason != "waiting_cloud" {
		t.Fatalf("pending view wrong: %+v", views)
	}

	if _, err := store.SetResult(2001, PairResultRejected, "remote_not_enabled", 101); err != nil {
		t.Fatal(err)
	}
	views, err = BuildPairViews(stateDir, conf, home, 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.PeerID == 2001 {
			if v.State != PairStateRejected || v.Reason != "remote_not_enabled" {
				t.Fatalf("rejected view wrong: %+v", v)
			}
			return
		}
	}
	t.Fatal("rejected view missing")
}

func TestPairViewRevokingWhenIntentOffButProjectionStale(t *testing.T) {
	home := t.TempDir()
	stateDir := writePairState(t, 3000, 5, 1900000000)
	conf := writeProjection(t, adRow(2000, 3000))
	store, _ := LoadPairIntents(home)
	if _, err := store.Set(2000, true, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(2000, false, 101); err != nil {
		t.Fatal(err)
	}

	views, err := BuildPairViews(stateDir, conf, home, 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].State != PairStateRevoking {
		t.Fatalf("want revoking, got %+v", views)
	}
}

func TestPairViewDegradedOnExpiredLease(t *testing.T) {
	home := t.TempDir()
	stateDir := writePairState(t, 3000, 5, 1900000000)
	conf := writeProjection(t, adRow(2000, 3000))
	store, _ := LoadPairIntents(home)
	if _, err := store.Set(2000, true, 100); err != nil {
		t.Fatal(err)
	}

	// now > lease expiry: the pair exists but must not be reported as usable.
	views, err := BuildPairViews(stateDir, conf, home, 2000000000)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].State != PairStateDegraded || views[0].Reason != "lease_expired" {
		t.Fatalf("want degraded/lease_expired, got %+v", views)
	}
}

func TestPairViewFallbackForDirectProjection(t *testing.T) {
	home := t.TempDir()
	stateDir := writePairState(t, 3000, 5, 1900000000)
	conf := writeProjection(t, directRow(2002, 3001))
	views, err := BuildPairViews(stateDir, conf, home, 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].PeerID != 2002 || views[0].State != PairStateFallback {
		t.Fatalf("want fallback view, got %+v", views)
	}
}

func TestPairViewSkipsEmptyProjectionAndNoIntent(t *testing.T) {
	views, err := BuildPairViews(t.TempDir(), filepath.Join(t.TempDir(), "missing.conf"), t.TempDir(), 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 0 {
		t.Fatalf("want no views, got %+v", views)
	}
}

func TestPairViewRejectsNonCanonicalProjection(t *testing.T) {
	home := t.TempDir()
	conf := writeProjection(t, strings.Replace(adRow(2000, 3000), " 19710 ", " 0 ", 1))
	if _, err := BuildPairViews(t.TempDir(), conf, home, 1800000000); err == nil {
		t.Fatal("non-canonical A-D projection must be rejected")
	}
}
