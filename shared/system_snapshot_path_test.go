package shared

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

func TestSnapshotPathUsesSystemHomeWithoutCreatingDirectories(t *testing.T) {
	t.Setenv("MYNET_HOME", "")
	t.Setenv("MYNET_DATA_HOME", "")
	if got, want := DefaultSnapshotPath(), filepath.Join(utils.PlatformDefaultHome(), "var", "system.json"); got != want {
		t.Fatalf("snapshot=%q, want %q", got, want)
	}
	home, data := filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "data")
	t.Setenv("MYNET_HOME", home)
	if got := VarDir(); got != filepath.Join(home, "var") {
		t.Fatalf("home var=%q", got)
	}
	t.Setenv("MYNET_DATA_HOME", data)
	if got := VarDir(); got != data {
		t.Fatalf("data override=%q, want %q", got, data)
	}
	for _, path := range []string{home, data} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("path resolution wrote %s: %v", path, err)
		}
	}
}
