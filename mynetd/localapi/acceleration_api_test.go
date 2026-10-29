package localapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

func TestDesktopAccelerationPutGetAndApply(t *testing.T) {
	s := NewServer(Config{ProxyConfDir: t.TempDir()})
	st := &domain.ProxyIntentState{}
	s.ProxyLoad = func(string) (*domain.ProxyIntentState, error) { return st, nil }
	s.ProxySave = func(_ string, next *domain.ProxyIntentState) error { st = next; return nil }
	var commands [][]string
	s.Ops = func(argv []string) (any, error) {
		commands = append(commands, argv)
		if len(argv) == 3 && argv[2] == "status" {
			return map[string]any{"stdout": "state=stopped"}, nil
		}
		return map[string]any{"stdout": "state=running"}, nil
	}

	body := bytes.NewBufferString(`{"enabled":true,"dns_enabled":true,"dns_sources":["tenant","clean_public"],"custom_domains":[" Example.COM. ","example.com"]}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/acceleration", body)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	if st.Desktop == nil || len(st.Desktop.CustomDomains) != 1 || st.Desktop.CustomDomains[0] != "example.com" {
		t.Fatalf("desktop config=%+v", st.Desktop)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/acceleration/apply", nil)
	rr = httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || len(commands) != 2 || commands[1][2] != "start" {
		t.Fatalf("apply status=%d commands=%v body=%s", rr.Code, commands, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/acceleration", nil)
	rr = httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	var response struct {
		Success bool                             `json:"success"`
		Data    domain.DesktopAccelerationConfig `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil || !response.Success || response.Data.Enabled != true {
		t.Fatalf("GET status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestDesktopAccelerationMigratesLegacyLocalPolicy(t *testing.T) {
	s := NewServer(Config{ProxyConfDir: t.TempDir()})
	s.ProxyLoad = func(string) (*domain.ProxyIntentState, error) {
		return &domain.ProxyIntentState{Local: &domain.StampedProxyPolicy{Policy: domain.ProxyPolicy{
			Enabled:     true,
			DomainRules: &domain.ProxyDomainRules{CustomDomains: []string{"Legacy.Example.com"}},
		}}}, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/acceleration", nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	var response struct {
		Data domain.DesktopAccelerationConfig `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Data.Enabled || len(response.Data.CustomDomains) != 1 || response.Data.CustomDomains[0] != "legacy.example.com" {
		t.Fatalf("migration result=%+v", response.Data)
	}
	// dns_enabled 必须跟随 enabled：否则 apply 侧 enabled && dns_enabled 为 false，
	// hook 被停掉，表现为「加速已启用但未生效」。
	if !response.Data.DNSEnabled {
		t.Fatalf("migration dns_enabled=false, want true when enabled=true: %+v", response.Data)
	}
}

func TestDesktopAccelerationApplyIsIdempotent(t *testing.T) {
	s := NewServer(Config{ProxyConfDir: t.TempDir()})
	s.ProxyLoad = func(string) (*domain.ProxyIntentState, error) {
		return &domain.ProxyIntentState{Desktop: &domain.DesktopAccelerationConfig{Enabled: true}}, nil
	}
	var commands [][]string
	s.Ops = func(argv []string) (any, error) {
		commands = append(commands, argv)
		// dns_enabled=false：加速只管路由，不启 hook；若 hook 残留则 stop
		return map[string]any{"stdout": "state=stopped"}, nil
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/acceleration/apply", nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || len(commands) != 1 || commands[0][2] != "status" {
		t.Fatalf("status=%d commands=%v body=%s", rr.Code, commands, rr.Body.String())
	}
}

func TestParseDesktopHookStatusWithExitProbe(t *testing.T) {
	got := parseDesktopHookStatus("dns-hook: pid=42 alive=true wanted=true state=running active=true listen_default=127.0.0.1:5353 pins=2 domains=7 routes=3\nexit_probe: node=1001 target=10.182.236.180 driver=gnb method=gnb_map link=direct reachable=true rtt_ms=1.5 age_ms=200 fails=0\nlast_error=none")
	if got["alive"] != true {
		t.Fatalf("alive=%v", got["alive"])
	}
	probe, ok := got["exit_probe"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 exit_probe: %#v", got)
	}
	if probe["node_id"] != "1001" {
		t.Fatalf("node_id=%v", probe["node_id"])
	}
	if probe["target"] != "10.182.236.180" {
		t.Fatalf("target=%v", probe["target"])
	}
	if probe["driver"] != "gnb" || probe["method"] != "gnb_map" || probe["link"] != "direct" {
		t.Fatalf("probe meta=%#v", probe)
	}
	if probe["reachable"] != true {
		t.Fatalf("reachable=%v", probe["reachable"])
	}
	if probe["rtt_ms"] != 1.5 {
		t.Fatalf("rtt_ms=%v", probe["rtt_ms"])
	}
	if probe["age_ms"] != int64(200) {
		t.Fatalf("age_ms=%v", probe["age_ms"])
	}
}

func TestParseDesktopHookStatusExitProbeUnreachable(t *testing.T) {
	got := parseDesktopHookStatus("dns-hook: pid=1 alive=true wanted=true state=running active=true listen_default=127.0.0.1:53 pins=0 domains=1 routes=1\nexit_probe: node=1001 target=10.0.0.8 driver=gnb method=gnb_map link=offline reachable=false rtt_ms=0 age_ms=-1 fails=3")
	probe := got["exit_probe"].(map[string]any)
	if probe["reachable"] != false {
		t.Fatalf("reachable=%v", probe["reachable"])
	}
	if probe["fails"] != 3 {
		t.Fatalf("fails=%v", probe["fails"])
	}
}

func TestParseDesktopHookStatusWithoutExitProbeLine(t *testing.T) {
	got := parseDesktopHookStatus("dns-hook: pid=42 alive=true wanted=true state=running active=true listen_default=127.0.0.1:5353 pins=2 domains=7 routes=3")
	if _, ok := got["exit_probe"]; ok {
		t.Fatalf("无 exit_probe 行时不应编造字段: %#v", got["exit_probe"])
	}
	if got["state"] != "running" || got["pid"] != 42 || got["domains"] != 7 || got["active"] != true {
		t.Fatalf("parsed status=%v", got)
	}
}

func TestParseDesktopHookStatusUnknown(t *testing.T) {
	got := parseDesktopHookStatus("dns-hook unavailable")
	if got["state"] != "unknown" {
		t.Fatalf("parsed status=%v", got)
	}
}

func TestDesktopAccelerationInstallSelectsPCRuntime(t *testing.T) {
	s := NewServer(Config{ProxyConfDir: t.TempDir()})
	var got []string
	s.Ops = func(argv []string) (any, error) {
		got = argv
		return map[string]any{"stdout": "runtime: pc"}, nil
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/acceleration/install", nil)
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || len(got) != 3 || got[0] != "config" || got[1] != "form" || got[2] != "pc" {
		t.Fatalf("status=%d argv=%v body=%s", rr.Code, got, rr.Body.String())
	}
}

func TestDesktopAccelerationRejectsInvalidDomain(t *testing.T) {
	s := NewServer(Config{ProxyConfDir: t.TempDir()})
	s.ProxyLoad = func(string) (*domain.ProxyIntentState, error) { return &domain.ProxyIntentState{}, nil }
	req := httptest.NewRequest(http.MethodPut, "/api/v1/acceleration", bytes.NewBufferString(`{"custom_domains":["https://example.com"]}`))
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}
