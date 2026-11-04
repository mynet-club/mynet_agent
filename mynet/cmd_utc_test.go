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
