//go:build !windows

package mynet

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/driver"
)

func TestUTCServiceLauncherReplacesProcess(t *testing.T) {
	if os.Getenv("MYNET_UTC_LAUNCH_TEST_HELPER") == "1" {
		args := []string{"utc", "driver-run", "--binary", os.Getenv("MYNET_UTC_TEST_BINARY"), "-c", os.Getenv("MYNET_UTC_TEST_CONF")}
		if os.Getenv("MYNET_UTC_TEST_SYSTEMD") == "1" {
			args = append(args, "--systemd")
		}
		os.Exit(Run(args))
	}
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("MYNET_HOME", root)
			conf := filepath.Join(root, "conf")
			if err := os.Mkdir(conf, 0700); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(root, "gnb")
			script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'Features: proxy utc-conf-v1'; exit 0; fi\nprintf '%s\\n' \"$$\" \"$@\"\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if managed {
				d := driver.NewGNBDriver(1)
				d.ConfDir = conf
				if err := d.MarkUTCManaged(); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command(os.Args[0], "-test.run=^TestUTCServiceLauncherReplacesProcess$")
			command.Env = append(os.Environ(), "MYNET_UTC_LAUNCH_TEST_HELPER=1", "MYNET_UTC_TEST_BINARY="+binary, "MYNET_UTC_TEST_CONF="+conf, "MYNET_UTC_TEST_SYSTEMD=1")
			output, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			want := []string{strconv.Itoa(command.Process.Pid), "-c", conf}
			if managed {
				want = append(want, "--utc-managed")
			}
			want = append(want, "--systemd")
			if strings.Join(lines, "\n") != strings.Join(want, "\n") {
				t.Fatalf("launcher changed PID or arguments: got %q want %q", lines, want)
			}
		})
	}
}

// Even well-formed legacy input must be rejected as an unknown command, not
// parsed as material configuration. No material, cursor, trust or state may change.
func TestUTCMaterialCommandRemoved(t *testing.T) {
	if raw := os.Getenv("MYNET_UTC_REMOVED_TEST_ARGS"); raw != "" {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			os.Exit(99)
		}
		os.Exit(Run(args))
	}
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		// A fixed old mtime catches rewrites that preserve the bytes.
		stamp := time.Unix(1000000000, 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, name := range []string{"conf/lease-a.json", "conf/lease-b.json", "conf/relay.trust", "conf/token", "conf/token.cursor", "conf/next", "conf/material.revision", "state/7.floor"} {
		write(name, "sentinel: "+name+"\n")
	}
	ca := write("ca.pem", "untouched CA sentinel\n")
	cert := write("client.pem", "untouched certificate sentinel\n")
	key := write("client.key", "untouched key sentinel, not a credential\n")
	legacy, err := json.Marshal(map[string]any{
		"url": "https://127.0.0.1:1/material", "ca": ca, "certificate": cert,
		"private_key": key, "server_pin": strings.Repeat("a", 64),
		"dir": filepath.Join(root, "conf"), "state_dir": filepath.Join(root, "state"),
		"transport": "a", "restart_command": []string{"/bin/false"},
		"trust_path":   filepath.Join(root, "conf/relay.trust"),
		"token_a_file": filepath.Join(root, "conf/token"), "next_a_file": filepath.Join(root, "conf/next"),
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := write("legacy config.json", string(legacy))
	badJSON := write("bad.json", "{not json")
	incomplete := write("incomplete.json", `{"url":"https://127.0.0.1:1/material"}`)
	type fileState struct {
		Body     string
		Mode     fs.FileMode
		Modified time.Time
	}
	snapshot := func() map[string]fileState {
		t.Helper()
		result := make(map[string]fileState)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			state := fileState{Mode: info.Mode()}
			if !entry.IsDir() {
				body, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				state.Body, state.Modified = string(body), info.ModTime()
			}
			result[path] = state
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := snapshot()
	cases := []struct {
		name string
		args []string
	}{
		{"utc without subcommand", []string{"utc"}},
		{"material without config", []string{"utc", "material"}},
		{"material unknown flag", []string{"utc", "material", "--bogus"}},
		{"material extra args", []string{"utc", "material", "-c", valid, "extra"}},
		{"material missing file", []string{"utc", "material", "-c", filepath.Join(root, "absent.json")}},
		{"material bad json", []string{"utc", "material", "--once", "-c", badJSON}},
		{"material incomplete config", []string{"utc", "material", "--once", "-c", incomplete}},
		{"material valid legacy once", []string{"utc", "material", "--once", "-c", valid}},
		{"material valid legacy daemon", []string{"utc", "material", "-c", valid}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			args, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUTCMaterialCommandRemoved$")
			cmd.Env = append(os.Environ(), "MYNET_UTC_REMOVED_TEST_ARGS="+string(args))
			cmd.WaitDelay = time.Second
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("removed command did not return promptly: %v; %s", ctx.Err(), output)
			}
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
				t.Errorf("Run(%v): error %v, want unknown-command exit 2; %s", tc.args, err, output)
			}
			if after := snapshot(); !reflect.DeepEqual(after, before) {
				t.Fatal("removed command changed the configuration/material sentinel tree")
			}
		})
	}
}
