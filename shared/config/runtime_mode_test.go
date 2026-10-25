package config

import (
	"os"
	"testing"
)

func TestRuntimeModePlatformMatrix(t *testing.T) {
	for _, osName := range []string{"linux", "freebsd", "openbsd", "netbsd"} {
		for _, selected := range []string{"pc", "gw"} {
			mode, err := ResolveRuntimeMode(osName, false, selected)
			if err != nil || mode != selected {
				t.Fatalf("%s/%s: %s %v", osName, selected, mode, err)
			}
		}
		mode, err := ResolveRuntimeMode(osName, false, "")
		if err != nil || mode != "gw" {
			t.Fatal("legacy gateway mode must be preserved")
		}
	}
	for _, osName := range []string{"darwin", "windows"} {
		if mode, err := ResolveRuntimeMode(osName, false, ""); err != nil || mode != "pc" {
			t.Fatal(mode, err)
		}
		if _, err := ResolveRuntimeMode(osName, false, "gw"); err == nil {
			t.Fatal("desktop gateway accepted")
		}
	}
	if _, err := ResolveRuntimeMode("linux", true, "pc"); err == nil {
		t.Fatal("OpenWrt pc accepted")
	}
	if mode, err := ResolveRuntimeMode("linux", true, ""); err != nil || mode != "gw" {
		t.Fatal(mode, err)
	}
	if _, err := ResolveRuntimeMode("linux", false, "auto"); err == nil {
		t.Fatal("unknown mode accepted")
	}
}
func TestRuntimeModeStoreIsExplicitAndRejectsCorruption(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_CONFIG_HOME", "")
	if err := SaveRuntimeMode("linux", false, "pc"); err != nil {
		t.Fatal(err)
	}
	if mode, err := LoadRuntimeMode("linux", false); err != nil || mode != "pc" {
		t.Fatal(mode, err)
	}
	if err := SaveRuntimeMode("linux", false, "gw"); err != nil {
		t.Fatal(err)
	}
	if mode, err := LoadRuntimeMode("linux", false); err != nil || mode != "gw" {
		t.Fatal(mode, err)
	}
	if err := os.WriteFile(RuntimeModePath(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeMode("linux", false); err == nil {
		t.Fatal("corrupt mode silently accepted")
	}
}
