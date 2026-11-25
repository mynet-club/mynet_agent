package endpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPairIntentStorePersistsAndReloads(t *testing.T) {
	home := t.TempDir()
	store, err := LoadPairIntents(home)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("expected empty store, got %d", len(got))
	}
	if _, err := store.Set(42, true, 1000); err != nil {
		t.Fatalf("set on: %v", err)
	}
	if _, err := store.SetResult(42, PairResultPending, "waiting_cloud", 1001); err != nil {
		t.Fatalf("set result: %v", err)
	}
	if _, err := store.Set(43, false, 1002); err != nil {
		t.Fatalf("set off: %v", err)
	}

	reloaded, err := LoadPairIntents(home)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	list := reloaded.List()
	if len(list) != 2 || list[0].PeerID != 42 || list[1].PeerID != 43 {
		t.Fatalf("unexpected intents: %+v", list)
	}
	if !list[0].Enabled() || list[0].LastResult != PairResultPending || list[0].Reason != "waiting_cloud" {
		t.Fatalf("intent 42 not persisted: %+v", list[0])
	}
	if list[1].Enabled() {
		t.Fatalf("intent 43 should be off: %+v", list[1])
	}

	path := PairIntentPath(home)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("intent file mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(home, "conf", "local")); err != nil {
		t.Fatalf("intent dir missing: %v", err)
	}
}

func TestPairIntentStoreRejectsInvalidInput(t *testing.T) {
	home := t.TempDir()
	store, err := LoadPairIntents(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(0, true, 1); err == nil {
		t.Fatal("peer 0 must be rejected")
	}
	if _, err := store.SetResult(7, PairResultActive, "", 1); err == nil {
		t.Fatal("result for unknown peer must be rejected")
	}
}

func TestPairIntentReloadRejectsMalformedFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "conf", "local"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PairIntentPath(home), []byte(`{"version":1,"pairs":[{"peer_id":0,"intent":"on"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPairIntents(home); err == nil {
		t.Fatal("peer_id 0 in file must be rejected")
	}
}
