//go:build !windows

package mynet

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPrepareGUIValidatesBeforeReplacing(t *testing.T) {
	root := t.TempDir()
	src, dest := filepath.Join(root, "spa"), filepath.Join(root, "embedded")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(wantOK bool) {
		t.Helper()
		out, err := exec.Command("sh", "../scripts/prepare-gui.sh", src, dest).CombinedOutput()
		if (err == nil) != wantOK {
			t.Fatalf("prepare GUI: err=%v output=%s", err, out)
		}
	}
	write(filepath.Join(dest, "stale.js"), "previous asset")
	assertPrevious := func() {
		t.Helper()
		if _, err := os.Stat(filepath.Join(dest, "stale.js")); err != nil {
			t.Fatalf("invalid input destroyed previous GUI: %v", err)
		}
	}
	run(false) // missing index
	assertPrevious()
	write(filepath.Join(src, "index.html"), `<script src="/assets/main.js"></script>`)
	run(false) // standalone-site prefix
	assertPrevious()
	write(filepath.Join(src, "index.html"), `<script src="/gui/assets/main.js"></script><link href="/gui/assets/main.css">`)
	write(filepath.Join(src, "assets", "main.js"), "console.log('fixture')")
	run(false) // referenced CSS absent, although JS exists
	assertPrevious()
	write(filepath.Join(src, "assets", "main.css"), "body{}")
	run(true)
	if _, err := os.Stat(filepath.Join(dest, "assets", "main.css")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "stale.js")); !os.IsNotExist(err) {
		t.Fatalf("stale asset retained: %v", err)
	}
	run(true) // repeat build is idempotent
}
