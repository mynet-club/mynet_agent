package commands

import (
	"strings"
	"testing"
)

func TestUTCCoordinatePlanIsRestricted(t *testing.T) {
	plan := `{"operation_id":"op-1"}`
	argv, ok := Plan(Request{Op: OpUTCCoordinate, Args: map[string]string{
		"action": "run", "plan_json": plan,
		// A request may smuggle these keys; they must never reach the executor.
		"install": "/tmp/evil-install.json", "trust": "/tmp/evil-trust.json",
		"gnb_binary": "/tmp/evil-gnb", "conf_dir": "/tmp",
	}}, "7")
	if !ok {
		t.Fatal("run with plan_json must be translatable")
	}
	joined := strings.Join(argv, " ")
	for _, forbidden := range []string{"evil-install", "evil-trust", "evil-gnb", "--install", "--trust", "--binary"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("request-controlled install/trust/binary leaked into argv: %v", argv)
		}
	}
	if !strings.Contains(joined, "--plan-json") {
		t.Fatalf("plan content missing from argv: %v", argv)
	}

	for _, action := range []string{"resume", "status"} {
		argv, ok := Plan(Request{Op: OpUTCCoordinate, Args: map[string]string{
			"action": action, "scope": "lab", "pair": "9"}}, "7")
		if !ok || !strings.Contains(strings.Join(argv, " "), "--scope lab --pair 9") {
			t.Fatalf("%s mapping = %v %v", action, argv, ok)
		}
	}
	if _, ok := Plan(Request{Op: OpUTCCoordinate, Args: map[string]string{"action": "destroy"}}, "7"); ok {
		t.Fatal("unknown action must be refused")
	}
	if _, ok := Plan(Request{Op: OpUTCCoordinate, Args: map[string]string{"action": "run"}}, "7"); ok {
		t.Fatal("run without plan content must be refused")
	}
	if _, ok := Plan(Request{Op: OpUTCCoordinate, Args: map[string]string{
		"action": "run", "plan_json": strings.Repeat("x", 131073)}}, "7"); ok {
		t.Fatal("oversized plan content must be refused")
	}
	if _, ok := Plan(Request{Op: OpUTCCoordinate, Args: map[string]string{"action": "resume", "scope": "lab"}}, "7"); ok {
		t.Fatal("resume without pair must be refused")
	}
	if op, ok := OpFromBackCommand("mynet.utc.coordinate"); !ok || op != OpUTCCoordinate {
		t.Fatalf("backend command mapping = %q %v", op, ok)
	}
	if _, ok := OpFromBackCommand("utc.coordinate"); ok {
		t.Fatal("command without the mynet. prefix must be refused")
	}
}
