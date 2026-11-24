package mynet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectToRunPreservesRuntimeUTCConfAndStatus(t *testing.T) {
	root := t.TempDir()
	confDir := filepath.Join(root, "conf", "online", "3722810996882027")
	runRoot := filepath.Join(root, "run")
	t.Setenv("MYNET_RUN_ROOT", runRoot)

	if err := os.MkdirAll(confDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "node.conf"), []byte("node-id 3722810996882027\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runDir := filepath.Join(runRoot, "3722810996882027")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	wantUTCConf := "utc-v2 mynet-prod nodeA 3722810996882027 7 boot 12292 3\n"
	wantUTCStatus := "status-v1 mynet-prod nodeA 3722810996882027 boot 7 1795487865 APPLIED 12292\n"
	if err := os.WriteFile(filepath.Join(runDir, "utc.conf"), []byte(wantUTCConf), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "utc.status"), []byte(wantUTCStatus), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "stale.conf"), []byte("remove-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	gotRunDir, err := projectToRun(confDir, "3722810996882027")
	if err != nil {
		t.Fatalf("projectToRun failed: %v", err)
	}
	if gotRunDir != runDir {
		t.Fatalf("runDir = %q, want %q", gotRunDir, runDir)
	}

	gotConf, err := os.ReadFile(filepath.Join(runDir, "utc.conf"))
	if err != nil {
		t.Fatalf("utc.conf was deleted by projectToRun: %v", err)
	}
	if string(gotConf) != wantUTCConf {
		t.Fatalf("utc.conf mutated: got %q, want %q", string(gotConf), wantUTCConf)
	}
	gotStatus, err := os.ReadFile(filepath.Join(runDir, "utc.status"))
	if err != nil {
		t.Fatalf("utc.status was deleted by projectToRun: %v", err)
	}
	if string(gotStatus) != wantUTCStatus {
		t.Fatalf("utc.status mutated: got %q, want %q", string(gotStatus), wantUTCStatus)
	}
	if _, err := os.Stat(filepath.Join(runDir, "stale.conf")); !os.IsNotExist(err) {
		t.Fatalf("stale.conf should have been cleaned up, got err=%v", err)
	}
}
