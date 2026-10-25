package mynet

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared"
)

func TestNftPersistenceRequiresActualMynetInclude(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`include "/etc/nftables.d/*.nft"`, true},
		{`include "/etc/nftables.d/mynet.nft";`, true},
		{`include "/etc/nftables.d/other.nft"`, false},
		{`# include "/etc/nftables.d/*.nft"`, false},
		{`include "/other/*.nft" # /etc/nftables.d/`, false},
	} {
		if got := nftablesConfLoadsDropIn(tc.body); got != tc.want {
			t.Errorf("%s: %v", tc.body, got)
		}
	}
}

func TestDisabledGatewayCannotReplayOldProxyArtifacts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")
	if err := os.MkdirAll(filepath.Join(home, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	paths := []string{FirewallNftPath(home), FirewallProxyNftPath(home), FirewallProxySetNftPath(home), filepath.Join(home, "scripts", "firewall.mynet_exit_fixture.nft")}
	for _, p := range paths {
		if err := os.WriteFile(p, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeGatewayProxyArtifacts(&shared.SystemSnapshot{OS: "linux"}); err != nil {
		t.Fatal(err)
	}
	for i, p := range paths {
		_, err := os.Stat(p)
		if (i == 1 || i == 2) && !os.IsNotExist(err) {
			t.Fatalf("disabled classifier remains: %s", p)
		}
		if (i == 0 || i == 3) && err != nil {
			t.Fatalf("independent firewall artifact removed: %s", p)
		}
	}
}

func TestLinuxPersistentLoaderUsesAllArtifacts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX firewall loader")
	}
	home := filepath.Join(t.TempDir(), "home with ' quotes")
	if err := os.MkdirAll(filepath.Join(home, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	// Exact loader path must be quoted; all nft artifacts share the same include.
	if !strings.Contains(firewallNftDropIn(home), filepath.Join(home, "scripts", "*.nft")) {
		t.Fatal("drop-in omits artifact set")
	}
	log := filepath.Join(home, "calls")
	t.Setenv("CALLS", log)
	if err := os.WriteFile(FirewallIncludePath(home), []byte("#!/bin/sh\necho replay >> \"$CALLS\"\nexit 17\n"), 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "plugin")
	if err := os.WriteFile(p, []byte(netfilterReplayPlugin(home)), 0755); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"save", "stop", "flush"} {
		if err := exec.Command("sh", p, action).Run(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("stop/save unexpectedly loaded rules")
	}
	for _, action := range []string{"start", "reload", "restart"} {
		err := exec.Command("sh", p, action).Run()
		if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 17 {
			t.Fatalf("loader failure lost: %v", err)
		}
	}
	got, _ := os.ReadFile(log)
	if string(got) != "replay\nreplay\nreplay\n" {
		t.Fatalf("wrong replay calls: %s", got)
	}
}

func TestFirewallIncludeReplayOrderAndFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX firewall loader")
	}
	home := t.TempDir()
	dir := filepath.Join(home, "scripts")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(home, "calls")
	// All commands stay in a fake PATH; no host rules are read or changed.
	fake := `#!/bin/sh
if [ "$1" = -f ] && [ "$2" = - ]; then
  dir=$(sed -n 's/^include "\(.*\)\/\*\.nft"$/\1/p')
  for file in "$dir"/*.nft; do
    name=$(basename "$file")
    echo "$name" >> "$CALLS"
    [ "$name" != "$FAIL_FILE" ] || exit 23
  done
else
  exit 99
fi
`

	if err := os.WriteFile(filepath.Join(home, "nft"), []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", home+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALLS", log)
	names := []string{FirewallNftArtifactName, FirewallProxySetNftName, FirewallProxyNftName, "firewall.mynet_exit_test.nft"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("# fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	body, err := firewallIncludeScript("tun0", "")
	if err != nil {
		t.Fatal(err)
	}
	path := FirewallIncludePath(home)
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if out, err := exec.Command("sh", path).CombinedOutput(); err != nil {
			t.Fatalf("replay: %v: %s", err, out)
		}
	}
	got, _ := os.ReadFile(log)
	want := strings.Join(names, "\n") + "\n"
	if string(got) != want+want {
		t.Fatalf("replay order/count: %s", got)
	}
	_ = os.Remove(log)
	if err := LoadNftArtifacts(home); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(log)
	if string(got) != want {
		t.Fatalf("Go loader differs from include: %s", got)
	}
	t.Setenv("FAIL_FILE", FirewallProxySetNftName)
	_ = os.Remove(log)
	if err := LoadNftArtifacts(home); err == nil {
		t.Fatal("Go loader swallowed artifact failure")
	}
	_ = os.Remove(log)
	out, err := exec.Command("sh", path).CombinedOutput()
	if err == nil {
		t.Fatalf("failed artifact reported success: %s", out)
	}
	got, _ = os.ReadFile(log)
	if string(got) != names[0]+"\n"+names[1]+"\n" {
		t.Fatalf("continued after failure: %s", got)
	}
}

func TestProxyExitReplayReplaceAndRevoke(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "scripts", "firewall."+proxyExitName("tun0")+".nft")
	if err := writeProxyExitReplayArtifact(home, "tun0", proxyExitScope{WAN: []string{"eth0"}}, exitTestPolicy("10.30.0.3/32")); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(first), "add table inet "+proxyExitName("tun0")+"\nflush table inet "+proxyExitName("tun0")+"\n") || !strings.Contains(string(first), `iifname "tun0" oifname "eth0" drop`) {
		t.Fatalf("not a cold/reload-safe ACL: %s", first)
	}
	if err := writeProxyExitReplayArtifact(home, "tun0", proxyExitScope{WAN: []string{"eth0"}}, exitTestPolicy("bad")); err == nil {
		t.Fatal("invalid policy accepted")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(first) {
		t.Fatal("invalid update replaced prior artifact")
	}
	if err := writeProxyExitReplayArtifact(home, "tun0", proxyExitScope{WAN: []string{"eth0"}}, exitTestPolicy("10.30.0.4/32")); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if strings.Contains(string(got), "10.30.0.3/32") || !strings.Contains(string(got), "10.30.0.4/32") {
		t.Fatal("old ACL retained")
	}
	if err := writeProxyExitReplayArtifact(home, "tun0", proxyExitScope{WAN: []string{"eth0"}}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if strings.Contains(string(got), "masquerade") || !strings.Contains(string(got), `iifname "tun0" oifname "eth0" drop`) {
		t.Fatal("revocation must replay WAN denial")
	}
}
