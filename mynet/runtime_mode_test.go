package mynet

import (
	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestPCModeUsesOSRoutesWithoutGatewayClassifier(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_CONFIG_HOME", "")
	p := &domain.ProxyPolicy{Enabled: true, Role: domain.ProxyRoleClient, Split: &domain.ProxySplit{MatchMode: domain.ProxyMatchNormal}, DNS: &domain.ProxyDNS{Mode: "redirect", Hijack: "both", RedirectTarget: "10.0.0.1"}, FullTunnel: true}
	for _, osName := range []string{"linux", "freebsd", "openbsd"} {
		if err := config.SaveRuntimeMode(osName, false, "pc"); err != nil {
			t.Fatal(err)
		}
		snap := &shared.SystemSnapshot{OS: osName, Capabilities: map[string]bool{"ip": true, "nft": true}}
		if builtForOpenWrt && osName == runtime.GOOS {
			if isPCProxyForm(snap) {
				t.Fatal("OpenWrt accepted PC mode")
			}
			continue
		}
		if !isPCProxyForm(snap) {
			t.Fatalf("%s pc not recognized", osName)
		}
		if cmds := proxyAllCmds(snap, p, ProxyRuntime{TunDevice: "tun0"}); len(cmds) != 0 {
			t.Fatalf("PC got gateway classifier: %v", cmds)
		}
		cmds := PCProxyPinCmds(snap, []string{"192.0.2.4"}, "tun0")
		text := joinCmds(cmds)
		if osName == "linux" && !strings.Contains(text, "ip route replace 192.0.2.4/32 dev tun0") { // GUARD-OK command-shape assertion, no system execution
			t.Fatal(text)
		}
		if osName != "linux" && !strings.Contains(text, "route -n add -host 192.0.2.4 -interface tun0") { // GUARD-OK command-shape assertion, no system execution
			t.Fatal(text)
		}
		if cmds := proxyCleanupCmds(snap, p, ProxyRuntime{}); len(cmds) != 0 {
			t.Fatal("PC cleanup must belong to session, not legacy gateway path")
		}
	}
	if err := config.SaveRuntimeMode("linux", false, "gw"); err != nil {
		t.Fatal(err)
	}
	snap := &shared.SystemSnapshot{OS: "linux", Capabilities: map[string]bool{"ip": true, "nft": true}}
	if isPCProxyForm(snap) {
		t.Fatal("GW treated as PC")
	}
	if len(proxyClientCmds(snap, p, ProxyRuntime{TunDevice: "tun0"})) == 0 {
		t.Fatal("GW classifier lost")
	}
}
func TestLinuxRouteSnapshotLongestPrefixAndMetrics(t *testing.T) {
	raw := []byte(`[{"dst":"default","dev":"eth0","metric":100},{"dst":"default","dev":"eth1","metric":200},{"dst":"192.0.2.0/24","dev":"tun0"},{"dst":"192.0.2.4","type":"blackhole"},{"dst":"203.0.113.9","dev":"tun0"}]`)
	view, err := parseLinuxPCRoutes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if view["0.0.0.0/0"] != "eth0" || !view.routed("192.0.2.5", "tun0") || view.routed("192.0.2.4", "tun0") || !view.routed("203.0.113.9", "tun0") {
		t.Fatal(view)
	}
}
func TestConfigFormReadDoesNotCreateConfiguration(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_CONFIG_HOME", "")
	if rc := cmdConfig([]string{"form", "--json"}); rc != 0 {
		t.Fatal(rc)
	}
	if _, err := os.Stat(config.RuntimeModePath()); !os.IsNotExist(err) {
		t.Fatal("read wrote configuration")
	}
}
