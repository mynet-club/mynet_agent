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
	"net/url"
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
	Trust       string `json:"trust"`
	TokensA     string `json:"tokens_a"`
	TokensB     string `json:"tokens_b"`
	NextA       string `json:"next_a"`
	NextB       string `json:"next_b"`
	PoolSeq     uint64 `json:"pool_seq"`
	UDPPort     uint32 `json:"udp_port"`
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
type bodyHolder struct{ v atomic.Value }

func materialServer(t *testing.T, status int, holder *bodyHolder) (*httptest.Server, *string) {
	t.Helper()
	seenTransport := new(string)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seenTransport = r.URL.RawQuery
		w.WriteHeader(status)
		body := holder.v.Load()
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
	body      *bodyHolder
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
	holder := &bodyHolder{}
	holder.v.Store(body)
	srv, transport := materialServer(t, status, holder)
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
	return &fixture{srv: srv, transport: transport, cfg: cfg, dir: dir, stateDir: stateDir, body: holder}
}

func (f *fixture) setBody(body any) { f.body.v.Store(body) }

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
	if q, _ := url.ParseQuery(*f.transport); q.Get("transport") != "relay-a" {
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
	body := validPayload(7, uint64(time.Now().Add(time.Hour).Unix()))
	body.Trust = "trust-v2"
	f := newFixture(t, http.StatusOK, body)
	f.cfg.RestartCommand = failureCommand(t)
	f.cfg.TrustPath = filepath.Join(f.dir, "relay.trust")
	if err := os.WriteFile(f.cfg.TrustPath, []byte("trust-v1"), 0600); err != nil {
		t.Fatal(err)
	}
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

// A1: a pool-only update on an installed revision applies in place (no
// restart), and a revision with unchanged trust is hot-applied as well.
func TestRunOnceHotAppliesPoolsAndRenewals(t *testing.T) {
	body := validPayload(7, uint64(time.Now().Add(time.Hour).Unix()))
	body.Trust = "trust-v1"
	body.TokensA = "ta1\nta2\n"
	body.NextA = "na1\n"
	body.TokensB = "tb1\n"
	body.NextB = "nb1\n"
	body.PoolSeq = 1
	f := newFixture(t, http.StatusOK, body)
	f.cfg.TrustPath = filepath.Join(f.dir, "relay.trust")
	f.cfg.TokenAFile = filepath.Join(f.dir, "token-a")
	f.cfg.TokenBFile = filepath.Join(f.dir, "token-b")
	f.cfg.NextAFile = filepath.Join(f.dir, "next-a")
	f.cfg.NextBFile = filepath.Join(f.dir, "next-b")
	if err := os.WriteFile(f.cfg.TrustPath, []byte("trust-v1"), 0600); err != nil {
		t.Fatal(err)
	}
	var restarts atomic.Int32
	a := f.agent(t)
	a.restart = func() error { restarts.Add(1); return nil }
	if err := a.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarts.Load() != 0 {
		t.Fatalf("restart ran on first install with unchanged trust: %d", restarts.Load())
	}
	if got := f.readFile(t, f.cfg.TokenAFile); got != "#window=1\nta1\nta2\n" { // GUARD-OK pool-window tag literal in test expectation
		t.Fatalf("token-a = %q", got)
	}
	if err := os.WriteFile(f.cfg.TokenAFile+".cursor", []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	// utcd rotates by copying next over token and deleting next; the agent
	// must restore next only, leaving the live (cursor-tracked) pool alone.
	if err := os.Remove(f.cfg.NextAFile); err != nil {
		t.Fatal(err)
	}
	body.PoolSeq = 2
	body.TokensA = "ta3\n"
	body.NextA = "na2\n"
	f.setBody(body)
	if err := a.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarts.Load() != 0 {
		t.Fatalf("pool replenishment restarted utcd: %d", restarts.Load())
	}
	if got := f.readFile(t, f.cfg.TokenAFile); got != "#window=1\nta1\nta2\n" { // GUARD-OK pool-window tag literal in test expectation
		t.Fatalf("live pool must not be rewritten = %q", got)
	}
	if got := f.readFile(t, f.cfg.NextAFile); got != "#window=2\nna2\n" {
		t.Fatalf("next pool = %q", got)
	}
	if _, err := os.Stat(f.cfg.TokenAFile + ".cursor"); err != nil {
		t.Fatal("cursor must survive a next-only replenishment")
	}
}

// A1 multi-pair: one agent and one utcd serve several pairs; each pair keeps
// its own revision, trust and pool sequence so replenishment stays hot.
func TestRunOnceMultiPairWritesPerPairPools(t *testing.T) {
	body := validPayload(5, uint64(time.Now().Add(time.Hour).Unix()))
	body.Trust = "trust-v1"
	body.TokensA = "pa1\npa2\n"
	body.NextA = "na1\n"
	body.TokensB = "pb1\npb2\n"
	body.NextB = "nb1\n"
	body.PoolSeq = 1
	f := newFixture(t, http.StatusOK, body)
	aDir, bDir := filepath.Join(f.dir, "p200"), filepath.Join(f.dir, "p201")
	f.cfg.Pairs = []PairMaterial{
		{PairID: 200, Side: "a", Dir: aDir, StateDir: filepath.Join(aDir, "state"),
			TrustPath: filepath.Join(aDir, "relay.trust"),
			TokenFile: filepath.Join(aDir, "token"), NextFile: filepath.Join(aDir, "next")},
		{PairID: 201, Side: "b", Dir: bDir, StateDir: filepath.Join(bDir, "state"),
			TrustPath: filepath.Join(bDir, "relay.trust"),
			TokenFile: filepath.Join(bDir, "token"), NextFile: filepath.Join(bDir, "next")},
	}
	a := f.agent(t)
	if err := a.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.readFile(t, filepath.Join(aDir, "token")); got != "#window=1\npa1\npa2\n" { // GUARD-OK pool-window tag literal in test expectation
		t.Fatalf("pair200 token = %q", got)
	}
	if got := f.readFile(t, filepath.Join(bDir, "token")); got != "#window=1\npb1\npb2\n" { // GUARD-OK pool-window tag literal in test expectation
		t.Fatalf("pair201 token = %q", got)
	}
	if got := f.readFile(t, filepath.Join(aDir, "relay.trust")); got != "trust-v1" {
		t.Fatalf("pair200 trust = %q", got)
	}
	for _, dir := range []string{aDir, bDir} {
		if got := f.readFile(t, filepath.Join(dir, "material.revision")); got != "5\n" {
			t.Fatalf("%s revision = %q", dir, got)
		}
	}
	// utcd rotates next -> token and deletes next; replenishment refills next.
	for _, p := range []string{filepath.Join(aDir, "next"), filepath.Join(bDir, "next")} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	body.PoolSeq = 2
	body.TokensA = "pa3\n"
	body.TokensB = "pb3\n"
	body.NextA = "na9\n"
	body.NextB = "nb9\n"
	f.setBody(body)
	var restarts atomic.Int32
	a.restart = func() error { restarts.Add(1); return nil }
	if err := a.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarts.Load() != 0 {
		t.Fatalf("hot pool swap restarted utcd: %d", restarts.Load())
	}
	if got := f.readFile(t, filepath.Join(aDir, "token")); got != "#window=1\npa1\npa2\n" { // GUARD-OK pool-window tag literal in test expectation
		t.Fatalf("pair200 live pool must not be rewritten = %q", got)
	}
	if got := f.readFile(t, filepath.Join(aDir, "next")); got != "#window=2\nna9\n" {
		t.Fatalf("pair200 next = %q", got)
	}
	if got := f.readFile(t, filepath.Join(bDir, "next")); got != "#window=2\nnb9\n" {
		t.Fatalf("pair201 next = %q", got)
	}
	if got := f.readFile(t, filepath.Join(aDir, "material.pool_seq")); got != "2\n" {
		t.Fatalf("pair200 pool seq = %q", got)
	}
}

func TestSupervisorRendersTemplate(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "utcd.tmpl")
	out := filepath.Join(dir, "utcd.ini")
	if err := os.WriteFile(tmpl, []byte("token={{pair200_token}}\ntrust={{pair200_trust}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sup := &supervisor{cfg: utcdConfig{Template: tmpl, Config: out}, logf: t.Logf}
	if err := sup.render(map[string]string{
		"pair200_token": "/etc/mynet/utc/relay/p200/token",
		"pair200_trust": "/etc/mynet/utc/relay/p200/relay.trust",
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "token=/etc/mynet/utc/relay/p200/token\ntrust=/etc/mynet/utc/relay/p200/relay.trust\n" {
		t.Fatalf("rendered = %q", raw)
	}
}

func TestSyncTrustLeasesCopiesDeliveredMaterial(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref-leases")
	if err := os.MkdirAll(ref, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lease-a.json"), []byte("new-a"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "revocations.json"), []byte("new-rev"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ref, "lease-a.json"), []byte("old-a"), 0600); err != nil {
		t.Fatal(err)
	}
	trust := filepath.Join(dir, "relay.trust")
	body := "[trust]\nlease_a_to_b=" + filepath.Join(ref, "lease-a.json") +
		"\nlease_b_to_a=" + filepath.Join(ref, "lease-b.json") +
		"\nrevocations=" + filepath.Join(ref, "revocations.json") + "\n"
	if err := os.WriteFile(trust, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := syncTrustLeases(PairMaterial{Dir: dir, TrustPath: trust}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(ref, "lease-a.json")); string(got) != "new-a" {
		t.Fatalf("lease not synced: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(ref, "revocations.json")); string(got) != "new-rev" {
		t.Fatalf("revocations not synced: %q", got)
	}
}

// Contract gate: the agent must ask the server for the slot it is about to
// install (1 on a cold start, local+1 when utcd consumed the next pool) and
// must include pair/side/base so multi-pair agents never share a pool.
func TestAgentRequestsSlotAndScopePerInstall(t *testing.T) {
	body := validPayload(9, uint64(time.Now().Add(time.Hour).Unix()))
	body.Trust = "trust-v1"
	body.TokensA = "q1\nq2\n"
	body.NextA = "q3\n"
	body.PoolSeq = 1
	f := newFixture(t, http.StatusOK, body)
	dir := filepath.Join(f.dir, "p300")
	f.cfg.Pairs = []PairMaterial{{
		PairID: 300, Side: "a", Dir: dir, StateDir: filepath.Join(dir, "state"),
		TrustPath: filepath.Join(dir, "client.trust"),
		TokenFile: filepath.Join(dir, "token"), NextFile: filepath.Join(dir, "next"),
		SlotBase: 240,
	}}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.cfg.Pairs[0].TrustPath, []byte("trust-v1"), 0600); err != nil {
		t.Fatal(err)
	}
	a := f.agent(t)
	if err := a.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertQuery := func(wantSlot string) {
		t.Helper()
		q, err := url.ParseQuery(*f.transport)
		if err != nil {
			t.Fatal(err)
		}
		if q.Get("pair") != "300" || q.Get("side") != "a" || q.Get("base") != "240" ||
			q.Get("slot") != wantSlot || q.Get("transport") != f.cfg.Transport {
			t.Fatalf("query = %q", *f.transport)
		}
	}
	assertQuery("1")
	if err := os.Remove(f.cfg.Pairs[0].NextFile); err != nil {
		t.Fatal(err)
	}
	body.PoolSeq = 2
	body.NextA = "q4\n"
	f.setBody(body)
	if err := a.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertQuery("2")
	if got := f.readFile(t, f.cfg.Pairs[0].NextFile); got != "#window=2\nq4\n" {
		t.Fatalf("next after replenish = %q", got)
	}
}

// Scale gate: one agent, many pairs — each pair keeps its own directory,
// trust and pool files, and a pair without a pending next is not rewritten.
func TestManyPairsStayIsolated(t *testing.T) {
	body := validPayload(3, uint64(time.Now().Add(time.Hour).Unix()))
	body.Trust = "shared-trust"
	body.TokensA = "ha\n"
	body.NextA = "hn\n"
	body.PoolSeq = 1
	f := newFixture(t, http.StatusOK, body)
	for pid := uint64(400); pid < 408; pid++ {
		dir := filepath.Join(f.dir, "p"+strconv.FormatUint(pid, 10))
		f.cfg.Pairs = append(f.cfg.Pairs, PairMaterial{
			PairID: pid, Side: "a", Dir: dir, StateDir: filepath.Join(dir, "state"),
			TrustPath: filepath.Join(dir, "client.trust"),
			TokenFile: filepath.Join(dir, "token"), NextFile: filepath.Join(dir, "next"),
			SlotBase: 240,
		})
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	a := f.agent(t)
	if err := a.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.cfg.Pairs {
		if got := f.readFile(t, p.TokenFile); got != "#window=1\nha\n" { // GUARD-OK pool-window tag literal in test expectation
			t.Fatalf("pair %d token = %q", p.PairID, got)
		}
		if got := f.readFile(t, p.TrustPath); got != "shared-trust" {
			t.Fatalf("pair %d trust = %q", p.PairID, got)
		}
		if got := f.readFile(t, filepath.Join(p.Dir, "material.pool_seq")); got != "1\n" {
			t.Fatalf("pair %d seq = %q", p.PairID, got)
		}
	}
}
