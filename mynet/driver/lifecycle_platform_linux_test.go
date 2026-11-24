//go:build linux && !openwrt

package driver

import (
	"os"
	"path/filepath"
	"testing"
)

// Even a plain Linux build (without -tags openwrt) running on OpenWrt/procd
// must detect /etc/init.d/mynet and delegate gnb lifecycle to procd so mynetd
// never spawns a second daemonized gnb competing for utc.lock.
func TestDetectOpenWrtServiceOwnerOnPlainLinuxBuild(t *testing.T) {
	dir := t.TempDir()
	initFile := filepath.Join(dir, "mynet")
	releaseFile := filepath.Join(dir, "openwrt_release")
	procdFile := filepath.Join(dir, "procd")

	if got := detectOpenWrtServiceOwner(initFile, releaseFile, procdFile); got != "" {
		t.Fatalf("missing init should return empty, got %q", got)
	}
	if err := os.WriteFile(initFile, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Init exists without OpenWrt markers (non-OpenWrt Linux): must NOT claim procd
	if got := detectOpenWrtServiceOwner(initFile, releaseFile, procdFile); got != "" {
		t.Fatalf("non-OpenWrt Linux should return empty, got %q", got)
	}
	// OpenWrt release marker present: must delegate to init
	if err := os.WriteFile(releaseFile, []byte("DISTRIB_ID='OpenWrt'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectOpenWrtServiceOwner(initFile, releaseFile, procdFile); got != initFile {
		t.Fatalf("OpenWrt should delegate to %q, got %q", initFile, got)
	}
}
