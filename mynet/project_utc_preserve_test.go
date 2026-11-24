package mynet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGnbConfPathUsesSingleConfDirWithoutProjection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_RUN_ROOT", filepath.Join(t.TempDir(), "should-not-be-used"))

	// Default fallback before setup
	if got, want := gnbConfPath(home), filepath.Join(home, "conf", "local", "default"); got != want {
		t.Fatalf("gnbConfPath(empty) = %q, want %q", got, want)
	}

	// Online node resolves directly to $MYNET_HOME/conf/online/<nodeId>
	if err := os.MkdirAll(filepath.Join(home, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "conf", "mode.json"),
		[]byte(`{"mode":"online","active":{"node_id":"3722810996882027"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := gnbConfPath(home), filepath.Join(home, "conf", "online", "3722810996882027"); got != want {
		t.Fatalf("gnbConfPath(online) = %q, want %q", got, want)
	}
}
