//go:build !windows

package mynet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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

// TestUTCMaterialArgs pins the CLI contract of `mynet utc material`: usage
// errors return 2, configuration errors return 1 and nothing touches the
// network (every case fails before a request can be issued).
func TestUTCMaterialArgs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	write := func(name, body string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	badJSON := write("bad.json", "{not json")
	incomplete := write("incomplete.json", `{"url":"https://127.0.0.1:1/material"}`)
	badPin := write("badpin.json", `{
		"url":"https://127.0.0.1:1/material","ca":"/nonexistent/ca.pem",
		"certificate":"/nonexistent/client.pem","private_key":"/nonexistent/client.key",
		"server_pin":"`+strings.Repeat("z", 64)+`","dir":"`+root+`/conf","state_dir":"`+root+`/state",
		"transport":"a","restart_command":["/bin/true"]}`)

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"utc without subcommand", []string{"utc"}, 2},
		{"material without config", []string{"utc", "material"}, 2},
		{"material unknown flag", []string{"utc", "material", "--bogus"}, 2},
		{"material extra args", []string{"utc", "material", "-c", incomplete, "extra"}, 2},
		{"material missing file", []string{"utc", "material", "-c", filepath.Join(root, "absent.json")}, 1},
		{"material bad json", []string{"utc", "material", "--once", "-c", badJSON}, 1},
		{"material incomplete config", []string{"utc", "material", "--once", "-c", incomplete}, 1},
		{"material non-hex pin", []string{"utc", "material", "--once", "-c", badPin}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Run(tc.args); got != tc.want {
				t.Fatalf("Run(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}
