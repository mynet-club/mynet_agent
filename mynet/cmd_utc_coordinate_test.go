package mynet

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeTestInstall(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "install.json")
	body := fmt.Sprintf(`{"version":1,"store_directory":%q,"nodes":[{"node_id":7,"instance":"node7",`+
		`"conf_dir":"/opt/mynet/conf/local/7","gnb_binary":"/opt/mynet/bin/gnb_es",`+
		`"utcd_socket":"/run/utc/control.sock"}]}`, filepath.Join(dir, "store"))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUTCCoordinateArgumentAndFailurePaths(t *testing.T) {
	if code := cmdUTCCoordinate(nil); code != 2 {
		t.Fatalf("empty args = %d", code)
	}
	if code := cmdUTCCoordinate([]string{"unknown"}); code != 2 {
		t.Fatalf("unknown action = %d", code)
	}
	if code := cmdUTCCoordinate([]string{"status", "--install", "/nonexistent/install.json",
		"--scope", "lab", "--pair", "9"}); code != 1 {
		t.Fatalf("missing install = %d", code)
	}
	install := writeTestInstall(t)
	if code := cmdUTCCoordinate([]string{"run", "--install", install}); code != 2 {
		t.Fatalf("run without plan content = %d", code)
	}
	if code := cmdUTCCoordinate([]string{"resume", "--install", install, "--trust", "/nonexistent/trust.json",
		"--scope", "lab", "--pair", "9"}); code != 1 {
		t.Fatalf("resume with missing trust = %d", code)
	}
	if code := cmdUTCCoordinate([]string{"status", "--install", install, "--scope", "lab", "--pair", "9"}); code != 1 {
		t.Fatalf("status without durable operation = %d", code)
	}
}
