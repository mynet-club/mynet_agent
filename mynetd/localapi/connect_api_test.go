package localapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func connectRequest(s *Server) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/connect", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}
func TestConnectFlow(t *testing.T) {
	for _, failure := range []string{"", "keys", "sync", "pull", "apply", "start", "routes"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("MYNET_HOME", t.TempDir())
			calls := []string{}
			s := NewServer(Config{Listen: "127.0.0.1:0"})
			s.Ops = func(argv []string) (any, error) {
				command := strings.Join(argv, " ")
				calls = append(calls, command)
				if command == "config status --json" {
					return opsOut(`{"initialized":true,"mode":"online","active":{"node_id":"42","driver":"wireguard"}}`), nil
				}
				stage := map[string]string{"config keys ensure --json": "keys", "config keys sync --connect --json": "sync", "config pull --download-only --json": "pull", "apply --active": "apply", "service start wireguard": "start", "apply --auto": "routes"}[command]
				if failure != "" && stage == failure {
					if stage == "keys" || stage == "sync" || stage == "pull" {
						return opsOut(`{"error":"test business failure"}`), nil
					}
					return map[string]any{"stderr": "test execution failure"}, errors.New("exit status 1")
				}
				return opsOut(`{}`), nil
			}
			rec := connectRequest(s)
			if failure == "" {
				if rec.Code != 200 || len(calls) != 7 {
					t.Fatalf("success: %d %v", rec.Code, calls)
				}
			} else {
				if rec.Code != 502 || !strings.Contains(rec.Body.String(), `"stage":"`+failure+`"`) {
					t.Fatalf("failure: %d %s", rec.Code, rec.Body.String())
				}
				if failure == "start" || failure == "routes" {
					if calls[len(calls)-1] != "service stop wireguard" {
						t.Fatalf("failed connection left running: %v", calls)
					}
				}
			}
		})
	}
}
func TestConnectMutationSerialization(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	s := NewServer(Config{Listen: "127.0.0.1:0"})
	s.Ops = func(argv []string) (any, error) {
		if strings.Join(argv, " ") == "config status --json" {
			close(entered)
			<-release
			return opsOut(`{"initialized":false}`), nil
		}
		if strings.Join(argv, " ") == "status" {
			return opsOut(`{"available":true}`), nil
		}
		t.Error("concurrent mutation invoked executor")
		return opsOut(`{}`), nil
	}
	go func() { defer close(done); connectRequest(s) }()
	<-entered
	for _, path := range []string{"/api/v1/config/connect", "/api/v1/config/keys", "/api/v1/config/active", "/api/v1/command"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"argv":["service","start","wireguard"]}`))
		req.Host = "127.0.0.1:8096"
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		if rec.Code != 409 || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("concurrent %s accepted: %d", path, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/command", strings.NewReader(`{"argv":["status"]}`))
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal("connect blocked a read-only command")
	}
	close(release)
	<-done
	if rec := connectRequestWithInvalidMode(); rec.Code != 400 {
		t.Fatalf("missing identity accepted: %d", rec.Code)
	}
}
func connectRequestWithInvalidMode() *httptest.ResponseRecorder {
	s := NewServer(Config{Listen: "127.0.0.1:0"})
	s.Ops = func(argv []string) (any, error) {
		return opsOut(`{"initialized":true,"mode":"local","active":{"node_id":"42","driver":"gnb"}}`), nil
	}
	return connectRequest(s)
}

func TestConfigReadMethods(t *testing.T) {
	s := NewServer(Config{Listen: "127.0.0.1:0"})
	s.Ops = func(argv []string) (any, error) { t.Fatal("invalid method reached executor"); return nil, nil }
	for _, path := range []string{"/api/v1/config/cloud", "/api/v1/config/identity", "/api/v1/config/runtime", "/api/v1/config/tenants", "/api/v1/plugins"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Host = "127.0.0.1:8096"
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		if rec.Code != 405 || rec.Header().Get("Allow") != "GET" {
			t.Fatalf("method not enforced: %s %d", path, rec.Code)
		}
	}
}

func TestConnectExpiredLogin(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	count := 0
	s := NewServer(Config{Listen: "127.0.0.1:0"})
	s.Ops = func(argv []string) (any, error) {
		count++
		if count == 1 {
			return opsOut(`{"initialized":true,"mode":"online","active":{"node_id":"42","driver":"gnb"}}`), nil
		}
		return opsOut(`{"error":"need_login"}`), nil
	}
	rec := connectRequest(s)
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), `"need_login":true`) || count != 2 {
		t.Fatalf("expired login continued: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConnectExecutionErrorIsRedacted(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	private := strings.Repeat("a", 128)
	s := NewServer(Config{Listen: "127.0.0.1:0"})
	s.Ops = func(argv []string) (any, error) {
		command := strings.Join(argv, " ")
		if command == "config status --json" {
			return opsOut(`{"initialized":true,"mode":"online","active":{"node_id":"42","driver":"gnb"}}`), nil
		}
		if command == "apply --active" {
			return map[string]any{"stderr": "private_hex=" + private}, errors.New("exit status 1")
		}
		return opsOut(`{}`), nil
	}
	rec := connectRequest(s)
	if rec.Code != 502 || strings.Contains(rec.Body.String(), private) {
		t.Fatalf("sensitive executor failure leaked: %d", rec.Code)
	}
	if !s.configMutation.TryLock() {
		t.Fatal("failed connect left mutation lock held")
	}
	s.configMutation.Unlock()
}
