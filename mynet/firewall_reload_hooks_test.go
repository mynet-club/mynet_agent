package mynet

import (
	"encoding/json"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUfwExternalReloadHookChainsAndRestoresOriginal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX hook")
	}
	home := filepath.Join(t.TempDir(), "home with 'quotes")
	path := filepath.Join(home, "ufw", "after.init")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(home, "calls")
	t.Setenv("CALLS", calls)
	original := []byte("#!/bin/sh\necho original:$1 >> \"$CALLS\"\nexit ${ORIGINAL_EXIT:-0}\n")
	if err := os.WriteFile(path, original, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(FirewallIncludePath(home), []byte("#!/bin/sh\necho replay >> \"$CALLS\"\nexit ${REPLAY_EXIT:-0}\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := installUfwReplayHook(home, path); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"start", "reload", "force-reload", "restart", "stop"} {
		if out, err := exec.Command("sh", path, action).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", action, err, out)
		}
	}
	got, _ := os.ReadFile(calls)
	want := "original:start\nreplay\noriginal:reload\nreplay\noriginal:force-reload\nreplay\noriginal:restart\nreplay\noriginal:stop\n"
	if string(got) != want {
		t.Fatalf("hook order: %s", got)
	}
	t.Setenv("ORIGINAL_EXIT", "19")
	err := exec.Command("sh", path, "reload").Run()
	if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 19 {
		t.Fatalf("original error lost: %v", err)
	}
	t.Setenv("ORIGINAL_EXIT", "0")
	t.Setenv("REPLAY_EXIT", "23")
	err = exec.Command("sh", path, "reload").Run()
	if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 23 {
		t.Fatalf("replay error lost: %v", err)
	}
	if err := removeUfwReplayHook(home, path); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatal("original UFW hook not restored")
	}
}

func TestServiceReloadHookKeepsNativeCommands(t *testing.T) {
	body := firewallServiceHook("/opt/my%net")
	if strings.Contains(body, "ExecReload=\n") || strings.Contains(body, "ExecStart=\n") {
		t.Fatal("native firewall commands reset")
	}
	if !strings.Contains(body, `ExecStartPost=/bin/sh "/opt/my%%net/scripts/firewall.mynet.include"`) || !strings.Contains(body, "ExecReload=/bin/sh") {
		t.Fatal(body)
	}
}

func TestProxyExitNoWANRejectsWithoutMutation(t *testing.T) {
	called := false
	if err := syncProxyExitFirewall(snapNft(), nil, "tun0", false, func(a []string, in string) (string, error) { called = true; return "", nil }); err == nil || called {
		t.Fatal("missing WAN silently changed firewall")
	}
	for _, p := range []*domain.ProxyPolicy{nil, exitTestPolicy(), exitTestPolicy("10.30.0.3/32")} {
		body, err := proxyExitNftBatch("tun0", proxyExitScope{WAN: []string{"eth0"}}, p, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "forward iifname") && !strings.Contains(line, "oifname") {
				t.Fatalf("LAN path restricted by WAN policy: %s", line)
			}
		}
	}
}

func TestProxyExitScopeMultiWANAndOnLinkLAN(t *testing.T) {
	snap := snapNft()
	snap.Detail = map[string]json.RawMessage{"routing": json.RawMessage(`{"default_iface":"br-lan","routes":[{"destination":"0.0.0.0/0","interface":"eth1"},{"destination":"::/0","interface":"wan6"},{"destination":"192.168.1.0/24","interface":"br-lan","scope":"link"},{"destination":"fd00:1::/64","interface":"wan6","protocol":"kernel"},{"destination":"203.0.113.0/24","interface":"br-lan","gateway":"192.168.1.254"},{"destination":"10.30.0.0/24","interface":"tun0","scope":"link"}]}`)}
	scope, err := proxyExitScopeFromSnapshot(snap, "tun0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(scope.WAN, ",") != "br-lan,eth1,wan6" || strings.Join(scope.LAN, ",") != "192.168.1.0/24,fd00:1::/64" {
		t.Fatalf("wrong exit scope: %+v", scope)
	}
	body, err := proxyExitNftBatch("tun0", scope, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `oifname { "br-lan", "eth1", "wan6" }`) || !strings.Contains(body, "ip daddr 192.168.1.0/24 return") || !strings.Contains(body, "ip6 daddr fd00:1::/64 return") {
		t.Fatal(body)
	}
}
