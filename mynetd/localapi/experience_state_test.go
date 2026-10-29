package localapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExperienceStateAggregatesInstallAndWizard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"credential.json", "mynet.conf"} {
		if err := os.WriteFile(filepath.Join(home, "conf", name), []byte("ok"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := NewServer(Config{})
	s.Ops = func(argv []string) (any, error) {
		if len(argv) > 0 && argv[0] == "node-status" {
			return map[string]any{"stdout": `{"state":"INSTALLED"}`}, nil
		}
		return map[string]any{"stdout": `{"node_id":"n1"}`}, nil
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/experience/state", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); !containsAll(got, `"state":"READY_OFFLINE"`, `"primary_action":"start"`) {
		t.Fatalf("unexpected response: %s", got)
	}
}

func containsAll(s string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(s, n) {
			return false
		}
	}
	return true
}

func TestExperienceStatePrefersRunningNodeOverStaleWizardFlag(t *testing.T) {
	// D4-E 后 conf/mynet.conf 不存在；隧道 RUNNING 时不得报 NEEDS_CONFIGURATION。
	wizard := map[string]any{"logged_in": true, "node_configured": false}
	node := map[string]any{"state": "RUNNING"}
	state, action := experienceState(wizard, node)
	if state != "READY" || action != "" {
		t.Fatalf("state=%s action=%s want READY", state, action)
	}

	// 真的没配也没跑 → 仍要报需要配置
	state, action = experienceState(wizard, map[string]any{"state": "UNAVAILABLE"})
	if state != "NEEDS_CONFIGURATION" || action != "configure" {
		t.Fatalf("state=%s action=%s want NEEDS_CONFIGURATION", state, action)
	}
}

func TestNodeConfiguredSeesD4ELayout(t *testing.T) {
	home := t.TempDir()
	conf := filepath.Join(home, "conf")
	if nodeConfigured(conf) {
		t.Fatal("empty conf should not be configured")
	}
	nodeDir := filepath.Join(conf, "online", "3764021915150630")
	if err := os.MkdirAll(nodeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeDir, "node.conf"), []byte("NODE_ID=3764021915150630\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !nodeConfigured(conf) {
		t.Fatal("D4-E conf/<form>/<nodeId>/node.conf should count as configured")
	}
}
