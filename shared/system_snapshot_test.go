package shared

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprintStableAndOrderIndependent(t *testing.T) {
	a := []IfaceFingerprint{{Name: "eth0", MAC: "aa", State: "up"}, {Name: "lo", MAC: "", State: "up"}}
	b := []IfaceFingerprint{{Name: "lo", MAC: "", State: "up"}, {Name: "eth0", MAC: "aa", State: "up"}}
	if ComputeFingerprint(a) != ComputeFingerprint(b) {
		t.Fatal("fingerprint must be order-independent")
	}
	c := []IfaceFingerprint{{Name: "eth0", MAC: "bb", State: "up"}, {Name: "lo", MAC: "", State: "up"}}
	if ComputeFingerprint(a) == ComputeFingerprint(c) {
		t.Fatal("fingerprint must change when MAC changes")
	}
}

func TestWriteReadSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "system.json")
	snap := &SystemSnapshot{
		OS:           "linux",
		Hostname:     "n1",
		Interfaces:   []IfaceFingerprint{{Name: "eth0", MAC: "aa", State: "up"}},
		Capabilities: map[string]bool{"ip": true},
	}
	if err := WriteSnapshot(path, snap); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint == "" || got.GeneratedAt.IsZero() {
		t.Fatalf("missing generated fields: %+v", got)
	}
	if NeedsRedetect(got, snap.Interfaces) {
		t.Fatal("same ifaces must not need redetect")
	}
	if !NeedsRedetect(got, []IfaceFingerprint{{Name: "wlan0", MAC: "cc", State: "up"}}) {
		t.Fatal("changed ifaces must need redetect")
	}
	// 原子写：不应留下 tmp
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("tmp left behind")
	}
}
