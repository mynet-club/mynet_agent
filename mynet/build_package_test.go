//go:build !windows

package mynet

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSourcesFingerprintTracksAssets(t *testing.T) {
	root := t.TempDir()
	assets, output := filepath.Join(root, "spa"), filepath.Join(root, "SOURCES.txt")
	if err := os.Mkdir(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	writeIndex := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(assets, "index.html"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(repo string, wantOK bool) string {
		t.Helper()
		out, err := exec.Command("sh", "../scripts/build-sources.sh", repo, "..", assets, "-", output).CombinedOutput()
		if (err == nil) != wantOK {
			t.Fatalf("sources err=%v output=%s", err, out)
		}
		if !wantOK {
			return ""
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	writeIndex("first build")
	first := run("..", true)
	if again := run("..", true); again != first {
		t.Fatal("same inputs produced different provenance")
	}
	writeIndex("second build")
	if changed := run("..", true); changed == first {
		t.Fatal("changed GUI content retained old fingerprint")
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	run(root, false) // a missing source repository must not silently record an empty SHA
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("invalid sources produced record: %v", err)
	}
}

func TestPackageRequiresCompleteInputsAndPreservesSources(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows"} {
		t.Run(platform, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "mynet_agent_"+platform+"_amd64_v0.0.0-test")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			suffix, ext := "", ".tgz"
			if platform == "windows" {
				suffix = ".exe"
			}
			if platform != "linux" {
				ext = ".zip"
			}
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			run := func(ok bool) {
				t.Helper()
				out, err := exec.Command("sh", "../scripts/pack.sh", dir).CombinedOutput()
				if (err == nil) != ok {
					t.Fatalf("pack err=%v output=%s", err, out)
				}
			}
			write("mynet"+suffix, "executor fixture")
			write("INSTALL.md", "install fixture")
			run(false)
			write("mynetd"+suffix, "daemon fixture")
			run(false) // provenance is mandatory
			if _, err := os.Stat(dir + ext); !os.IsNotExist(err) {
				t.Fatalf("invalid input produced archive: %v", err)
			}
			write("SOURCES.txt", "format=1\nagent_sha=fixture\n")
			run(true)
			verify := func(wantOK bool) {
				t.Helper()
				out, err := exec.Command("sh", "../scripts/check-package-sources.sh", dir+ext, filepath.Base(dir)+ext, filepath.Join(dir, "SOURCES.txt")).CombinedOutput()
				if (err == nil) != wantOK {
					t.Fatalf("published sources err=%v output=%s", err, out)
				}
			}
			verify(true)
			write("SOURCES.txt", "format=1\nagent_sha=different\n")
			verify(false) // HTTP 200 alone cannot prove an identical multi-repo release
			args := []string{"-xOf", dir + ext, filepath.Base(dir) + "/SOURCES.txt"}
			tool := "tar"
			if ext == ".zip" {
				tool = "unzip"
				args = []string{"-p", dir + ext, filepath.Base(dir) + "/SOURCES.txt"}
			}
			out, err := exec.Command(tool, args...).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "agent_sha=fixture") {
				t.Fatalf("archive provenance err=%v output=%s", err, out)
			}
		})
	}
}
