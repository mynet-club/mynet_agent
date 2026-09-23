package mynet

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mynet-club/mynet_agent-V2/shared"
)

func TestApplyPlanRouteArgv(t *testing.T) {
	r := RouteOp{Destination: "10.8.0.0/24", Device: "gnb0", Action: "replace"}
	argv := routeArgv(nil, r)
	want := []string{"ip", "route", "replace", "10.8.0.0/24", "dev", "gnb0"}
	if len(argv) != len(want) {
		t.Fatalf("argv=%v want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv=%v want %v", argv, want)
		}
	}
}

func TestFirewallMasqPicksToolFromSnapshot(t *testing.T) {
	snapNft := &shared.SystemSnapshot{Capabilities: map[string]bool{"nft": true, "iptables": true}}
	argv := firewallArgv(snapNft, FirewallOp{Action: "ensure-masq", Device: "wg0"})
	if argv[0] != "nft" {
		t.Fatalf("prefer nft, got %v", argv)
	}
	snapIpt := &shared.SystemSnapshot{Capabilities: map[string]bool{"iptables": true}}
	argv = firewallArgv(snapIpt, FirewallOp{Action: "ensure-masq", Device: "gnb0"})
	if argv[0] != "iptables" {
		t.Fatalf("prefer iptables, got %v", argv)
	}
	// 跨驱动：wg0 与 gnb0 对 apply 无差别
	if len(firewallArgv(snapIpt, FirewallOp{Action: "ensure-masq", Device: "wg0"})) == 0 {
		t.Fatal("wg0 must be treated same as gnb0")
	}
}

func TestExecuteApplyDryRun(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	plan := `{"routes":[{"destination":"10.9.0.0/24","device":"wg0","action":"replace"}]}`
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := readApplyPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Routes) != 1 {
		t.Fatalf("routes=%v", p.Routes)
	}
}

func TestRunHelp(t *testing.T) {
	if code := Run([]string{"help"}); code != 0 {
		t.Fatalf("help exit=%d", code)
	}
	if code := Run(nil); code != 2 {
		t.Fatalf("noargs exit=%d", code)
	}
}
