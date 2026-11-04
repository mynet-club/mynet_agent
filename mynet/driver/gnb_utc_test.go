//go:build !windows

package driver

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func utcTestDriver(t *testing.T) *GNBDriver {
	t.Helper()
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	conf := filepath.Join(root, "conf", "local", "7")
	if err := os.MkdirAll(conf, 0700); err != nil {
		t.Fatal(err)
	}
	return &GNBDriver{ConfDir: conf, Binary: "test-gnb", Exec: func(string, ...string) ([]byte, error) {
		return []byte("GNB\nFeatures: proxy utc-conf utc-conf-v1\n"), nil
	}}
}
func TestUTCManagedHistorySurvivesConfigurationLoss(t *testing.T) {
	d := utcTestDriver(t)
	if err := d.MarkUTCManaged(); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkUTCManaged(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(d.ConfDir); err != nil {
		t.Fatal(err)
	}
	args, err := d.utcStartArgs()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"-c", d.ConfDir, "--utc-managed"}) {
		t.Fatalf("unsafe arguments: %v", args)
	}
}
func TestUTCManagedHistoryRejectsCorruptionAndSymlink(t *testing.T) {
	for _, kind := range []string{"content", "permissions", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			d := utcTestDriver(t)
			if err := d.MarkUTCManaged(); err != nil {
				t.Fatal(err)
			}
			p, _, _ := d.utcHistoryPath()
			switch kind {
			case "content":
				if err := os.WriteFile(p, []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(p, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(p, p+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(p+".old", p); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := d.utcStartArgs(); err == nil {
				t.Fatal("ambiguous history allowed startup")
			}
		})
	}
}
func TestUTCManagedDriverCapabilityIsExact(t *testing.T) {
	d := utcTestDriver(t)
	if err := d.MarkUTCManaged(); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{"utc-conf-v1", "Features: utc-conf-v10", "Features: proxy utc-conf"} {
		d.Exec = func(string, ...string) ([]byte, error) { return []byte(output), nil }
		if _, err := d.utcStartArgs(); err == nil {
			t.Fatalf("accepted unsupported capability %q", output)
		}
	}
}
func TestUTCUntouchedNativeLaunchDoesNotProbeCapability(t *testing.T) {
	d := utcTestDriver(t)
	d.Exec = func(string, ...string) ([]byte, error) {
		t.Fatal("native start unexpectedly probed UTC")
		return nil, nil
	}
	args, err := d.utcStartArgs()
	if err != nil || !reflect.DeepEqual(args, []string{"-c", d.ConfDir}) {
		t.Fatalf("args=%v err=%v", args, err)
	}
}

func TestUTCManagedDirectSpawnCarriesHistoryAssertion(t *testing.T) {
	d := utcTestDriver(t)
	if err := d.MarkUTCManaged(); err != nil {
		t.Fatal(err)
	}
	called := false
	d.Spawn = func(bin string, args ...string) error {
		called = true
		if bin != "test-gnb" || !reflect.DeepEqual(args, []string{"-c", d.ConfDir, "--utc-managed"}) {
			t.Fatalf("unsafe spawn %s %v", bin, args)
		}
		return nil
	}
	if _, err := d.spawnGnb(d.Binary); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("spawn not called")
	}
}

func TestUTCManagedUnwiredServiceCannotBypassGuard(t *testing.T) {
	d := utcTestDriver(t)
	if err := d.MarkUTCManaged(); err != nil {
		t.Fatal(err)
	}
	previous := platformServiceOwnerFn
	platformServiceOwnerFn = func() string { return "/test/procd" }
	defer func() { platformServiceOwnerFn = previous }()
	d.Exec = func(string, ...string) ([]byte, error) {
		t.Fatal("delegated start bypassed UTC guard")
		return nil, nil
	}
	if err := d.startLocked(); err == nil {
		t.Fatal("unwired delegated startup accepted")
	}
}

func TestUTCManagedDelegatesOnlyThroughGuardedTemplate(t *testing.T) {
	d := utcTestDriver(t)
	if err := d.MarkUTCManaged(); err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(t.TempDir(), "mynet.init")
	if err := os.WriteFile(owner, []byte("# MYNET_UTC_LAUNCH_V1\nprocd_set_param command mynet utc driver-run --binary gnb -c conf\n"), 0700); err != nil {
		t.Fatal(err)
	}
	previous := platformServiceOwnerFn
	platformServiceOwnerFn = func() string { return owner }
	defer func() { platformServiceOwnerFn = previous }()
	var actions []string
	d.Exec = func(name string, args ...string) ([]byte, error) {
		if name != owner || len(args) != 1 {
			t.Fatalf("unexpected delegation %q %v", name, args)
		}
		actions = append(actions, args[0])
		return nil, nil
	}
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	if err := d.Reload(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actions, []string{"start", "restart"}) {
		t.Fatalf("actions %v", actions)
	}
	// The same historically managed instance must reject a downgraded template
	// BEFORE a restart can stop its working GNB process.
	if err := os.WriteFile(owner, []byte("procd_set_param command gnb -c conf\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := d.Reload(); err == nil {
		t.Fatal("legacy restart bypassed guard")
	}
	if len(actions) != 2 {
		t.Fatal("unsafe restart was invoked")
	}
}

func TestUTCManagedChecksEffectiveSystemdCommand(t *testing.T) {
	d := utcTestDriver(t)
	d.ServiceUnit = "mynet_gnb.service"
	previous := platformServiceOwnerFn
	platformServiceOwnerFn = func() string { return "" }
	defer func() { platformServiceOwnerFn = previous }()
	for _, test := range []struct {
		command string
		valid   bool
	}{
		{"{ path=/bin/mynet ; argv[]=/bin/mynet utc driver-run --binary /bin/gnb -c " + d.ConfDir + " --systemd ; }", true},
		{"{ path=/bin/gnb ; argv[]=/bin/gnb -c " + d.ConfDir + " --systemd ; }", false},
		{"{ path=/bin/mynet ; argv[]=/bin/mynet utc driver-run --binary /bin/gnb -c /other/node --systemd ; }", false},
	} {
		d.Exec = func(name string, args ...string) ([]byte, error) {
			if name != "systemctl" {
				t.Fatalf("unexpected %s", name)
			}
			if reflect.DeepEqual(args, []string{"show", "-p", "LoadState", "--value", d.ServiceUnit}) {
				return []byte("loaded"), nil
			}
			if !reflect.DeepEqual(args, []string{"show", d.ServiceUnit, "--property=ExecStart", "--value"}) {
				t.Fatalf("must inspect effective ExecStart, got %v", args)
			}
			return []byte(test.command), nil
		}
		if err := d.utcCheckDelegation(); (err == nil) != test.valid {
			t.Fatalf("command=%q err=%v", test.command, err)
		}
	}
}

func TestUTCHistoryCannotBeBypassedThroughDirectoryAlias(t *testing.T) {
	d := utcTestDriver(t)
	alias := filepath.Join(filepath.Dir(d.ConfDir), "alias")
	if err := os.Symlink(d.ConfDir, alias); err != nil {
		t.Fatal(err)
	}
	if err := d.MarkUTCManaged(); err != nil {
		t.Fatal(err)
	}
	alternate := *d
	alternate.ConfDir = alias
	if managed, err := alternate.utcManaged(); err != nil || !managed {
		t.Fatalf("alias lost history: %v %v", managed, err)
	}
	if err := os.RemoveAll(d.ConfDir); err != nil {
		t.Fatal(err)
	}
	// A dangling alias cannot be treated as a new native instance.
	if _, err := alternate.utcStartArgs(); err == nil {
		t.Fatal("dangling alias bypassed recovery")
	}
	if managed, err := d.utcManaged(); err != nil || !managed {
		t.Fatalf("canonical history lost after deletion %v %v", managed, err)
	}
}
