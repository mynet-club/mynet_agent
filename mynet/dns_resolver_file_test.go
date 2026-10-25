package mynet

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func resolverFixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")
	path := filepath.Join(home, "resolv.conf")
	original := "# original\nsearch corp.test\noptions timeout:2\nnameserver 192.0.2.53\n"
	if err := os.WriteFile(path, []byte(original), 0640); err != nil {
		t.Fatal(err)
	}
	return path, original
}
func TestResolverFileRestoresFullContentAndPermissions(t *testing.T) {
	path, original := resolverFixture(t)
	ok := func() error { return nil }
	if err := applyResolverFileDNS(path, []string{"127.0.0.1"}, func(string) error { return nil }, ok); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "# original\nsearch corp.test\noptions timeout:2\nnameserver 127.0.0.1\n" {
		t.Fatalf("resolver options lost: %s", raw)
	}
	if err := RestoreSystemDNS(); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(raw) != original || info.Mode().Perm() != 0640 || DNSRestoreNeeded() {
		t.Fatal("file not fully restored")
	}
}
func TestResolverFilePreservesSymlinkAndDoesNotWriteManagerTarget(t *testing.T) {
	target, original := resolverFixture(t)
	path := target + ".link"
	if err := os.Symlink(filepath.Base(target), path); err != nil {
		t.Fatal(err)
	}
	if err := applyResolverFileDNS(path, []string{"127.0.0.1"}, func(string) error { return nil }, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != original {
		t.Fatal("wrote through manager-owned symlink")
	}
	if err := RestoreSystemDNS(); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(path)
	if err != nil || link != filepath.Base(target) {
		t.Fatalf("symlink not restored: %s %v", link, err)
	}
}
func TestResolverFileRollbackAndUserEdit(t *testing.T) {
	path, original := resolverFixture(t)
	if err := applyResolverFileDNS(path, []string{"127.0.0.1"}, func(string) error { return nil }, func() error { return errors.New("verify failed") }); err == nil {
		t.Fatal("failure hidden")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != original || DNSRestoreNeeded() {
		t.Fatal("rollback failed")
	}
	if err := applyResolverFileDNS(path, []string{"127.0.0.1"}, func(string) error { return nil }, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	changed := "nameserver 203.0.113.53\n"
	if err := os.WriteFile(path, []byte(changed), 0644); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSystemDNS(); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if string(raw) != changed {
		t.Fatal("user edit overwritten")
	}
}
