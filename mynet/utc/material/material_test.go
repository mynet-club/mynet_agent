package material

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// payload mirrors the utc-materiald response document.
type payload struct {
	Revision    uint64 `json:"revision"`
	ExpiresAt   uint64 `json:"expires_at"`
	LeaseA      string `json:"lease_a"`
	LeaseB      string `json:"lease_b"`
	Revocations string `json:"revocations"`
	FloorName   string `json:"floor_name"`
	FloorB64    string `json:"floor_b64"`
}

func validPayload(revision, expires uint64) payload {
	return payload{
		Revision:    revision,
		ExpiresAt:   expires,
		LeaseA:      `{"lease":"a"}`,
		LeaseB:      `{"lease":"b"}`,
		Revocations: `{"epoch":1}`,
		FloorName:   "7.floor",
		FloorB64:    base64.StdEncoding.EncodeToString([]byte("floor-bytes")),
	}
}

// materialServer serves one payload (or raw bytes when payload is a string) and
// records the transport query it observed.
func materialServer(t *testing.T, status int, body any) (*httptest.Server, *string) {
	t.Helper()
	seenTransport := new(string)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seenTransport = r.URL.Query().Get("transport")
		w.WriteHeader(status)
		if raw, ok := body.(string); ok {
			_, _ = io.WriteString(w, raw)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, seenTransport
}

func serverPin(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

func writeCAPEM(t *testing.T, dir, name string, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(dir, name)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(path, pemBytes, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeClientKeyPair creates a throwaway client certificate; the materiald test
// server does not request client certificates, but the client must still hold a
// loadable pair because production materiald is mutually authenticated.
func writeClientKeyPair(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "utc-node"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "client.pem")
	keyPath := filepath.Join(dir, "client.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	derKey, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: derKey}), 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// successCommand is the restart_command used by tests: it must exist and succeed
// without touching a real utcd. The script is created inside t.TempDir(), not on
// a system path.
func successCommand(t *testing.T) []string {
	t.Helper()
	return restartScript(t, 0)
}

func failureCommand(t *testing.T) []string {
	t.Helper()
	return restartScript(t, 1)
}

func restartScript(t *testing.T, exitCode int) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", "exit " + strconv.Itoa(exitCode)}
	}
	path := filepath.Join(t.TempDir(), "restart.sh")
	body := "#!/bin/sh\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return []string{path}
}

type fixture struct {
	srv       *httptest.Server
	transport *string
	cfg       Config
	dir       string
	stateDir  string
}

// newFixture wires a TLS test server, a temporary install tree and a valid
// configuration pointing at both. pinOverrides lets a test break the pin.
func newFixture(t *testing.T, status int, body any, pinOverrides ...string) *fixture {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "conf")
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	srv, transport := materialServer(t, status, body)
	ca := writeCAPEM(t, root, "ca.pem", srv)
	cert, key := writeClientKeyPair(t, root)
	pin := serverPin(t, srv)
	if len(pinOverrides) > 0 {
		pin = pinOverrides[0]
	}
	cfg := Config{
		URL:               srv.URL + "/material",
		CA:                ca,
		Certificate:       cert,
		PrivateKey:        key,
		ServerPin:         pin,
		Dir:               dir,
		StateDir:          stateDir,
		Transport:         "relay-a",
		RestartCommand:    successCommand(t),
		PollSeconds:       60,
		RefreshBeforeSecs: 120,
	}
	return &fixture{srv: srv, transport: transport, cfg: cfg, dir: dir, stateDir: stateDir}
}

func (f *fixture) agent(t *testing.T) *Agent {
	t.Helper()
	a, err := New(f.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.logger = log.New(io.Discard, "", 0)
	return a
}

func (f *fixture) readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func (f *fixture) assertAbsent(t *testing.T, name string) {
	t.Helper()
	for _, path := range []string{filepath.Join(f.dir, name), filepath.Join(f.stateDir, name)} {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("%s should not exist", path)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	complete := func() Config {
		return Config{
			URL: "https://127.0.0.1:1/m", CA: "ca.pem", Certificate: "c.pem", PrivateKey: "k.pem",
			ServerPin: strings.Repeat("a", 64), Dir: "conf", StateDir: "state",
			Transport: "a", RestartCommand: []string{"/bin/true"},
		}
	}
	t.Run("missing fields", func(t *testing.T) {
		cfg := complete()
		cfg.StateDir = ""
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("err = %v, want incomplete config", err)
		}
	})
	t.Run("pin too short", func(t *testing.T) {
		cfg := complete()
		cfg.ServerPin = strings.Repeat("a", 63)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("err = %v, want incomplete config", err)
		}
	})
	t.Run("pin not hex", func(t *testing.T) {
		cfg := complete()
		cfg.ServerPin = strings.Repeat("z", 64)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "64 hex") {
			t.Fatalf("err = %v, want hex pin rejection", err)
		}
	})
	t.Run("defaults applied", func(t *testing.T) {
		cfg := complete()
		cfg.PollSeconds = 0
		cfg.RefreshBeforeSecs = -1
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if cfg.PollSeconds != DefaultPollSeconds || cfg.RefreshBeforeSecs != DefaultRefreshBeforeSecs {
			t.Fatalf("defaults = %d/%d, want %d/%d", cfg.PollSeconds, cfg.RefreshBeforeSecs,
				DefaultPollSeconds, DefaultRefreshBeforeSecs)
		}
	})
	t.Run("explicit poll values kept", func(t *testing.T) {
		cfg := complete()
		cfg.PollSeconds = 7
		cfg.RefreshBeforeSecs = 11
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if cfg.PollSeconds != 7 || cfg.RefreshBeforeSecs != 11 {
			t.Fatalf("poll values changed: %d/%d", cfg.PollSeconds, cfg.RefreshBeforeSecs)
		}
	})
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	valid := `{
		"url":"https://127.0.0.1:1/m","ca":"ca.pem","certificate":"c.pem","private_key":"k.pem",
		"server_pin":"` + strings.Repeat("a", 64) + `","dir":"conf","state_dir":"state",
		"transport":"a","restart_command":["/bin/true"]}`

	t.Run("defaults", func(t *testing.T) {
		cfg, err := LoadConfig(write("valid.json", valid))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.PollSeconds != DefaultPollSeconds || cfg.RefreshBeforeSecs != DefaultRefreshBeforeSecs {
			t.Fatalf("defaults = %d/%d", cfg.PollSeconds, cfg.RefreshBeforeSecs)
		}
		if cfg.Transport != "a" || cfg.Dir != "conf" {
			t.Fatalf("fields not decoded: %+v", cfg)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if _, err := LoadConfig(filepath.Join(dir, "absent.json")); err == nil {
			t.Fatal("missing config accepted")
		}
	})
	t.Run("bad json", func(t *testing.T) {
		if _, err := LoadConfig(write("bad.json", "{not json")); err == nil {
			t.Fatal("bad json accepted")
		}
	})
	t.Run("incomplete", func(t *testing.T) {
		if _, err := LoadConfig(write("incomplete.json", `{"url":"https://127.0.0.1:1/m"}`)); err == nil ||
			!strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("err = %v, want incomplete config", err)
		}
	})
}

func TestRunOnceInstallsMaterial(t *testing.T) {
	expires := uint64(time.Now().Add(time.Hour).Unix())
	f := newFixture(t, http.StatusOK, validPayload(7, expires))
	if err := f.agent(t).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if *f.transport != "relay-a" {
		t.Fatalf("transport query = %q", *f.transport)
	}
	if got := f.readFile(t, filepath.Join(f.dir, "lease-a.json")); got != `{"lease":"a"}` {
		t.Fatalf("lease-a.json = %q", got)
	}
	if got := f.readFile(t, filepath.Join(f.dir, "lease-b.json")); got != `{"lease":"b"}` {
		t.Fatalf("lease-b.json = %q", got)
	}
	if got := f.readFile(t, filepath.Join(f.dir, "revocations.json")); got != `{"epoch":1}` {
		t.Fatalf("revocations.json = %q", got)
	}
	if got := f.readFile(t, filepath.Join(f.dir, "material.revision")); got != "7\n" {
		t.Fatalf("material.revision = %q", got)
	}
	if got := f.readFile(t, filepath.Join(f.dir, "material.expires")); got != strconv.FormatUint(expires, 10)+"\n" {
		t.Fatalf("material.expires = %q, want %d", got, expires)
	}
	if got := f.readFile(t, filepath.Join(f.stateDir, "7.floor")); got != "floor-bytes" {
		t.Fatalf("floor = %q", got)
	}
	for _, path := range []string{
		filepath.Join(f.dir, "lease-a.json"),
		filepath.Join(f.dir, "material.revision"),
		filepath.Join(f.stateDir, "7.floor"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s mode = %v, want 0600", path, info.Mode().Perm())
		}
	}
}

func TestRunOncePinMismatchWritesNothing(t *testing.T) {
	// A wrong (but well-formed) pin must abort before any file is installed.
	f := newFixture(t, http.StatusOK, validPayload(7, uint64(time.Now().Add(time.Hour).Unix())),
		strings.Repeat("0", 64))
	err := f.agent(t).RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pin") {
		t.Fatalf("err = %v, want SPKI pin mismatch", err)
	}
	f.assertAbsent(t, "material.revision")
	f.assertAbsent(t, "lease-a.json")
	f.assertAbsent(t, "7.floor")
	if _, err := os.Stat(f.stateDir); err == nil {
		t.Fatalf("state dir %s must not be created on pin mismatch", f.stateDir)
	}
}

func TestRunOnceRevisionNotRegressing(t *testing.T) {
	expires := uint64(time.Now().Add(time.Hour).Unix())
	for _, tc := range []struct {
		name     string
		revision uint64
	}{
		{"equal revision", 9},
		{"older revision", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, http.StatusOK, validPayload(tc.revision, expires))
			if err := os.WriteFile(filepath.Join(f.dir, "material.revision"), []byte("9\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.dir, "material.expires"), []byte("1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var restarts atomic.Int32
			a := f.agent(t)
			a.restart = func() error { restarts.Add(1); return nil }
			if err := a.RunOnce(context.Background()); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			if got := f.readFile(t, filepath.Join(f.dir, "material.revision")); got != "9\n" {
				t.Fatalf("material.revision = %q, want 9", got)
			}
			if a.LocalRevision() != 9 {
				t.Fatalf("LocalRevision = %d", a.LocalRevision())
			}
			// The existing contract refreshes expires even without reinstall.
			if a.LocalExpiry() != expires {
				t.Fatalf("LocalExpiry = %d, want %d", a.LocalExpiry(), expires)
			}
			if restarts.Load() != 0 {
				t.Fatalf("restart ran %d times for a duplicate revision", restarts.Load())
			}
			f.assertAbsent(t, "lease-a.json")
			f.assertAbsent(t, "lease-b.json")
		})
	}
}

func TestRunOnceRejectsBadResponse(t *testing.T) {
	t.Run("bad json", func(t *testing.T) {
		f := newFixture(t, http.StatusOK, "{not json")
		if err := f.agent(t).RunOnce(context.Background()); err == nil {
			t.Fatal("bad json accepted")
		}
		f.assertAbsent(t, "material.revision")
	})
	t.Run("missing lease", func(t *testing.T) {
		body := validPayload(7, uint64(time.Now().Add(time.Hour).Unix()))
		body.LeaseB = ""
		f := newFixture(t, http.StatusOK, body)
		err := f.agent(t).RunOnce(context.Background())
		if err == nil || !strings.Contains(err.Error(), "incomplete material response") {
			t.Fatalf("err = %v, want incomplete material response", err)
		}
		f.assertAbsent(t, "material.revision")
	})
	t.Run("server error", func(t *testing.T) {
		f := newFixture(t, http.StatusInternalServerError, "boom")
		err := f.agent(t).RunOnce(context.Background())
		if err == nil || !strings.Contains(err.Error(), "material server") {
			t.Fatalf("err = %v, want material server error", err)
		}
		f.assertAbsent(t, "material.revision")
	})
}

func TestRunOnceRestartFailureKeepsInstalledFiles(t *testing.T) {
	f := newFixture(t, http.StatusOK, validPayload(7, uint64(time.Now().Add(time.Hour).Unix())))
	f.cfg.RestartCommand = failureCommand(t)
	err := f.agent(t).RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("err = %v, want restart failure", err)
	}
	// Files are installed before the restart, so the next poll can retry the
	// restart instead of refetching, and utcd already sees a consistent set.
	if got := f.readFile(t, filepath.Join(f.dir, "material.revision")); got != "7\n" {
		t.Fatalf("material.revision = %q", got)
	}
	if got := f.readFile(t, filepath.Join(f.dir, "lease-a.json")); got != `{"lease":"a"}` {
		t.Fatalf("lease-a.json = %q", got)
	}
	if got := f.readFile(t, filepath.Join(f.stateDir, "7.floor")); got != "floor-bytes" {
		t.Fatalf("floor = %q", got)
	}
}

func TestNewRejectsIncompleteConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("empty config accepted")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	expires := uint64(time.Now().Add(time.Hour).Unix())
	f := newFixture(t, http.StatusOK, validPayload(3, expires))
	a := f.agent(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for a.LocalRevision() != 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.LocalRevision() != 3 {
		cancel()
		t.Fatal("initial sync never installed revision 3")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run ignored context cancellation")
	}
}
