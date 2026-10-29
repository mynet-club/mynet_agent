package mynet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirectIPExempted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	if err := saveHookDirectIPs([]string{"123.125.246.121", "111.206.0.0/16"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "var", "proxy", "dns-hook-direct-ips.json")); err != nil {
		t.Fatalf("file missing: %v", err)
	}
	if !directIPExempted("123.125.246.121") {
		t.Fatal("exact IP should be exempt")
	}
	if !directIPExempted("111.206.191.150") {
		t.Fatal("range should be exempt")
	}
	if directIPExempted("119.188.175.46") {
		t.Fatal("unlisted IP must not be exempt")
	}
}
