// Package material pulls short-lived UTC relay material from utc-materiald,
// installs it atomically and restarts the local utcd.
//
// It is shared by the standalone cmd/utc-material-agent daemon and the
// `mynet utc material` subcommand. The on-disk contract is unchanged:
// lease-a.json, lease-b.json, revocations.json, material.revision and
// material.expires live under dir, the revocation floor lives under state_dir.
//
// Semantics (kept from the original daemon):
//   - TLS 1.3 only; the relay certificate is pinned by SPKI sha256 in addition
//     to the configured CA. Hostname SANs are not required (relay may be
//     addressed by IP or by a certificate issued for another name).
//   - Revisions are monotonic per node. A response with revision <= the
//     locally installed revision is not reinstalled (the issuer rotates before
//     expiry, so the local copy is authoritative); only material.expires is
//     refreshed when it moved.
//   - Every file is written through a temp file + rename in the target
//     directory, so utcd never observes a partial file.
//   - The restart command runs only after all files are installed. A restart
//     failure is returned as an error but leaves the installed files intact
//     (the next poll rewrites them).
package material

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LogPrefix prefixes every log line and configuration error produced by this
// package; it is part of the operator-facing surface of both entry points.
const LogPrefix = "utc-material-agent: "

// DefaultPollSeconds and DefaultRefreshBeforeSecs are applied by Validate when
// the configuration leaves the field unset or non-positive.
const (
	DefaultPollSeconds       = 300
	DefaultRefreshBeforeSecs = 1200
)

// Config mirrors the utc-material-agent JSON configuration. Field names and
// defaults are wire-compatible with the original daemon.
type Config struct {
	URL               string   `json:"url"`
	CA                string   `json:"ca"`
	Certificate       string   `json:"certificate"`
	PrivateKey        string   `json:"private_key"`
	ServerPin         string   `json:"server_pin"`
	Dir               string   `json:"dir"`
	StateDir          string   `json:"state_dir"`
	Transport         string   `json:"transport"`
	RestartCommand    []string `json:"restart_command"`
	PollSeconds       int64    `json:"poll_seconds"`
	RefreshBeforeSecs int64    `json:"refresh_before_seconds"`
	// A1: the trust file and token pools travel with the material. Empty
	// paths keep the previous behaviour (leases only, operator-provided
	// trust/pools).
	TrustPath  string `json:"trust_path"`
	TokenAFile string `json:"token_a_file"`
	TokenBFile string `json:"token_b_file"`
	NextAFile  string `json:"next_a_file"`
	NextBFile  string `json:"next_b_file"`
	// UTCD hands utcd's lifecycle to the agent: the configuration is rendered
	// from the template and the process is started/restarted by this agent.
	UTCD utcdConfig `json:"utcd"`
	// Pairs lists every pair this node terminates. When empty the legacy
	// single-pair fields above are used as one pair.
	Pairs []PairMaterial `json:"pairs"`
	// SlotBase skips already-consumed leading tokens of the placement spec.
	SlotBase uint64 `json:"slot_base"`
}

// PairMaterial is one pair's local material wiring. Side is "a" or "b" (empty
// keeps the legacy behaviour of writing both token pools).
type PairMaterial struct {
	PairID    uint64 `json:"pair_id"`
	Side      string `json:"side"`
	Dir       string `json:"dir"`
	StateDir  string `json:"state_dir"`
	TrustPath string `json:"trust_path"`
	TokenFile string `json:"token_file"`
	NextFile  string `json:"next_file"`
	// SlotBase skips tokens already consumed from the placement spec head.
	SlotBase uint64 `json:"slot_base"`
}

// pairList resolves the configured pairs, falling back to the legacy
// single-pair layout so existing configurations keep working.
func (c *Config) pairList() []PairMaterial {
	if len(c.Pairs) > 0 {
		return c.Pairs
	}
	return []PairMaterial{{
		Dir: c.Dir, StateDir: c.StateDir, TrustPath: c.TrustPath,
		TokenFile: c.TokenAFile, NextFile: c.NextAFile,
	}}
}

func (p PairMaterial) label() string {
	if p.PairID == 0 {
		return "material"
	}
	return fmt.Sprintf("pair%d", p.PairID)
}

// LoadConfig reads, decodes and validates path. Polling defaults are applied
// by Validate, so the returned Config is ready for New.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects incomplete configuration and fills polling defaults. It is
// idempotent and safe to call on a Config built by hand.
func (c *Config) Validate() error {
	if c.URL == "" || c.CA == "" || c.Certificate == "" || c.PrivateKey == "" ||
		c.ServerPin == "" || c.Dir == "" || c.StateDir == "" || c.Transport == "" ||
		len(c.ServerPin) != 64 {
		return errors.New(LogPrefix + "config is incomplete")
	}
	// A pin that is not 64 hex characters can never match a certificate, so
	// reject it here instead of failing on every poll.
	if _, err := hex.DecodeString(c.ServerPin); err != nil {
		return errors.New(LogPrefix + "server_pin must be 64 hex characters")
	}
	if c.PollSeconds <= 0 {
		c.PollSeconds = DefaultPollSeconds
	}
	if c.RefreshBeforeSecs <= 0 {
		c.RefreshBeforeSecs = DefaultRefreshBeforeSecs
	}
	return nil
}

// response is the utc-materiald JSON payload.
type response struct {
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

// Agent is a material client bound to one validated configuration.
type Agent struct {
	cfg     Config
	client  *http.Client
	restart func() error
	utcd    *supervisor
	logger  *log.Logger
}

// New validates cfg and prepares the mutual-TLS client. It performs no I/O
// beyond reading the CA and key pair.
func New(cfg Config) (*Agent, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	client, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	agent := &Agent{cfg: cfg, client: client, logger: log.Default()}
	sup, err := newSupervisor(cfg.UTCD, agent.logf)
	if err != nil {
		return nil, err
	}
	agent.utcd = sup
	agent.restart = func() error {
		if sup != nil {
			return sup.restart()
		}
		if len(cfg.RestartCommand) == 0 {
			return nil
		}
		return runCommand(cfg.RestartCommand)
	}
	return agent, nil
}

func newClient(c Config) (*http.Client, error) {
	cert, err := tls.LoadX509KeyPair(c.Certificate, c.PrivateKey)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pem, err := os.ReadFile(c.CA)
	if err != nil || !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("cannot load CA")
	}
	pin, err := hex.DecodeString(c.ServerPin)
	if err != nil {
		return nil, errors.New(LogPrefix + "server_pin must be 64 hex characters")
	}
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
		// The relay certificate is pinned by SPKI; hostname SANs are optional.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("missing server certificate")
			}
			parsed, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			sum := sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
			if !bytes.Equal(sum[:], pin) {
				return errors.New("server SPKI pin mismatch")
			}
			return nil
		},
	}
	transport := &http.Transport{TLSClientConfig: tlsCfg}
	return &http.Client{Transport: transport, Timeout: 20 * time.Second}, nil
}

// SetLogger redirects the agent log lines (used by mynetd's logger).
func (a *Agent) SetLogger(f func(string, ...any)) {
	if f == nil {
		return
	}
	a.logger = log.New(logWriter{f}, "", 0)
}

type logWriter struct{ f func(string, ...any) }

func (w logWriter) Write(p []byte) (int, error) {
	w.f("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// RunOnce performs exactly one fetch/install cycle and returns its error.
func (a *Agent) RunOnce(ctx context.Context) error { return a.sync(ctx) }

// Run performs an initial cycle and then polls until ctx is cancelled. Sync
// failures are logged, never fatal; the returned error is ctx.Err().
func (a *Agent) Run(ctx context.Context) error {
	if err := a.sync(ctx); err != nil {
		a.logf("initial sync failed: %v", err)
	}
	basePoll := time.Duration(a.cfg.PollSeconds) * time.Second
	fastPoll := 30 * time.Second
	for {
		wait := basePoll
		if exp := a.LocalExpiry(); exp > 0 &&
			exp <= uint64(time.Now().Unix())+uint64(a.cfg.RefreshBeforeSecs) && wait > fastPoll {
			wait = fastPoll
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if err := a.sync(ctx); err != nil {
			a.logf("sync failed: %v", err)
		}
	}
}

func (a *Agent) sync(ctx context.Context) error {
	trustChanged := false
	for _, pair := range a.cfg.pairList() {
		changed, err := a.syncPair(ctx, pair)
		if err != nil {
			return fmt.Errorf("%s: %w", pair.label(), err)
		}
		trustChanged = trustChanged || changed
	}
	if err := a.renderUTCD(); err != nil {
		return err
	}
	if trustChanged {
		if err := a.restart(); err != nil {
			return err
		}
		a.logf("trust changed, utcd restarted")
		return nil
	}
	return a.ensureUTCD()
}

// syncPair fetches and installs one pair and reports whether its trust changed.
func (a *Agent) syncPair(ctx context.Context, pair PairMaterial) (bool, error) {
	localRev := localRevisionIn(pair.Dir)
	parsed, err := url.Parse(a.cfg.URL)
	if err != nil {
		return false, err
	}
	q := parsed.Query()
	q.Set("transport", a.cfg.Transport)
	if pair.PairID != 0 {
		q.Set("pair", strconv.FormatUint(pair.PairID, 10))
	}
	if pair.Side != "" {
		q.Set("side", pair.Side)
	}
	base := pair.SlotBase
	if base == 0 {
		base = a.cfg.SlotBase
	}
	if base != 0 {
		q.Set("base", strconv.FormatUint(base, 10))
	}
	slot := localPoolSeqIn(pair.Dir)
	if slot == 0 || (pair.TokenFile != "" && !fileExists(pair.TokenFile)) {
		slot = 1
	} else if pair.NextFile != "" && !fileExists(pair.NextFile) {
		// utcd copied next over the live pool and deleted it: the client must
		// supply the following slot without touching the live token file.
		slot++
	}
	q.Set("slot", strconv.FormatUint(slot, 10))
	parsed.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return false, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, fmt.Errorf("material server: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var material response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&material); err != nil {
		return false, err
	}
	if material.Revision == 0 || material.LeaseA == "" || material.LeaseB == "" ||
		material.Revocations == "" || material.FloorName == "" || material.FloorB64 == "" {
		return false, errors.New("incomplete material response")
	}
	// The issuer rotates before expiry, so a revision already installed is
	// authoritative; re-installing it would restart utcd on every poll.
	if material.Revision <= localRev {
		if localExpiryIn(pair.Dir) != material.ExpiresAt {
			_ = writeAtomic(filepath.Join(pair.Dir, "material.expires"),
				[]byte(strconv.FormatUint(material.ExpiresAt, 10)+"\n"), 0600)
		}
		// A newer pool on an already-installed revision is applied in place:
		// utcd reads the pool per attempt and the cursor restarts at the head,
		// so replenishment never restarts the data plane.
		return false, a.applyPools(&material, pair)
	}
	floor, err := base64.StdEncoding.DecodeString(material.FloorB64)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(pair.Dir, 0700); err != nil {
		return false, err
	}
	if err := os.MkdirAll(pair.StateDir, 0700); err != nil {
		return false, err
	}
	files := map[string][]byte{
		filepath.Join(pair.Dir, "lease-a.json"):                         []byte(material.LeaseA),
		filepath.Join(pair.Dir, "lease-b.json"):                         []byte(material.LeaseB),
		filepath.Join(pair.Dir, "revocations.json"):                     []byte(material.Revocations),
		filepath.Join(pair.StateDir, filepath.Base(material.FloorName)): floor,
	}
	for path, data := range files {
		if err := writeAtomic(path, data, 0600); err != nil {
			return false, err
		}
	}
	trustChanged := false
	if material.Trust != "" && pair.TrustPath != "" {
		previous, _ := os.ReadFile(pair.TrustPath)
		if !bytes.Equal(previous, []byte(material.Trust)) {
			if err := writeAtomic(pair.TrustPath, []byte(material.Trust), 0600); err != nil {
				return false, err
			}
			trustChanged = true
		}
	}
	if err := a.applyPools(&material, pair); err != nil {
		return false, err
	}
	if err := syncTrustLeases(pair); err != nil {
		return false, err
	}
	if err := writeAtomic(filepath.Join(pair.Dir, "material.revision"),
		[]byte(strconv.FormatUint(material.Revision, 10)+"\n"), 0600); err != nil {
		return false, err
	}
	if err := writeAtomic(filepath.Join(pair.Dir, "material.expires"),
		[]byte(strconv.FormatUint(material.ExpiresAt, 10)+"\n"), 0600); err != nil {
		return false, err
	}
	_ = syncTrustLeases(pair)
	a.logf("%s installed revision=%d expires=%d trust_changed=%v (hot apply)",
		pair.label(), material.Revision, material.ExpiresAt, trustChanged)
	return trustChanged, nil
}

// materialPaths are the template variables the utcd configuration may use.
// Legacy single-pair names stay available; multi-pair templates use
// pair<N>_token / pair<N>_next / pair<N>_trust.
func (a *Agent) materialPaths() map[string]string {
	paths := map[string]string{
		"trust": a.cfg.TrustPath, "token_a": a.cfg.TokenAFile,
		"token_b": a.cfg.TokenBFile, "next_a": a.cfg.NextAFile,
		"next_b": a.cfg.NextBFile,
	}
	for _, p := range a.cfg.pairList() {
		if p.PairID == 0 {
			continue
		}
		prefix := fmt.Sprintf("pair%d", p.PairID)
		paths[prefix+"_token"] = p.TokenFile
		paths[prefix+"_next"] = p.NextFile
		paths[prefix+"_trust"] = p.TrustPath
		paths[prefix+"_dir"] = p.Dir
	}
	return paths
}

func (a *Agent) renderUTCD() error {
	if a.utcd == nil {
		return nil
	}
	return a.utcd.render(a.materialPaths())
}

// ensureUTCD renders the configuration and starts utcd when it is not running.
func (a *Agent) ensureUTCD() error {
	if a.utcd == nil {
		return nil
	}
	if err := a.utcd.render(a.materialPaths()); err != nil {
		return err
	}
	return a.utcd.ensure()
}

// applyPools installs the token pools and clears the per-pool cursors so utcd
// starts at the head of the fresh pool. Pools with the same sequence are a
// no-op, which keeps polls cheap.
func (a *Agent) applyPools(m *response, pair PairMaterial) error {
	seq := localPoolSeqIn(pair.Dir)
	first := seq == 0 || (pair.TokenFile != "" && !fileExists(pair.TokenFile))
	needNext := !first && pair.NextFile != "" && !fileExists(pair.NextFile)
	if !first && !needNext {
		return nil
	}
	write := func(path, data string, clearCursor bool) error {
		if path == "" || data == "" {
			return nil
		}
		if err := writeAtomic(path, []byte(data), 0600); err != nil {
			return err
		}
		if !clearCursor {
			return nil
		}
		if err := os.Remove(path + ".cursor"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	// utcd keys its pool rotation on the leading "#window=<N>" tag; a pool
	// without it parses as window zero, so the next Core watermark bump parks
	// the cursor and the pool is never used. The material server's pool_seq is
	// the window this slice belongs to.
	tag := ""
	if m.PoolSeq != 0 {
		tag = "#window=" + strconv.FormatUint(m.PoolSeq, 10) + "\n"
	}
	tagged := func(pool string) string {
		if pool == "" || strings.HasPrefix(pool, "#window=") {
			return pool
		}
		return tag + pool
	}
	apply := func(tokenPath, nextPath string, tokens, next string) error {
		if first {
			if err := write(tokenPath, tagged(tokens), true); err != nil {
				return err
			}
			if err := write(nextPath, tagged(next), true); err != nil {
				return err
			}
		} else if needNext {
			if err := write(nextPath, tagged(next), false); err != nil {
				return err
			}
		}
		return nil
	}
	switch pair.Side {
	case "a":
		if err := apply(pair.TokenFile, pair.NextFile, m.TokensA, m.NextA); err != nil {
			return err
		}
	case "b":
		if err := apply(pair.TokenFile, pair.NextFile, m.TokensB, m.NextB); err != nil {
			return err
		}
	default:
		// Legacy configuration: one node may carry both sides' pools.
		if first || needNext {
			if err := apply(a.cfg.TokenAFile, a.cfg.NextAFile, m.TokensA, m.NextA); err != nil {
				return err
			}
			if err := apply(a.cfg.TokenBFile, a.cfg.NextBFile, m.TokensB, m.NextB); err != nil {
				return err
			}
		}
	}
	if m.PoolSeq != 0 {
		if err := writeAtomic(filepath.Join(pair.Dir, "material.pool_seq"),
			[]byte(strconv.FormatUint(m.PoolSeq, 10)+"\n"), 0600); err != nil {
			return err
		}
	}
	if first {
		a.logf("%s pools installed slot=%d (no restart)", pair.label(), m.PoolSeq)
	} else {
		a.logf("%s next pool replenished slot=%d (no restart)", pair.label(), m.PoolSeq)
	}
	return nil
}

// syncTrustLeases copies the delivered lease/revocation files over the paths
// the installed trust references. The trust document is signed and cannot be
// rewritten, so the delivered material must land where it points; otherwise
// the two relays load different lease generations and refuse with STATE.
func syncTrustLeases(pair PairMaterial) error {
	raw, err := os.ReadFile(pair.TrustPath)
	if err != nil {
		return nil
	}
	wanted := map[string]string{
		"lease_a_to_b": "lease-a.json",
		"lease_b_to_a": "lease-b.json",
		"revocations":  "revocations.json",
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name, known := wanted[key]
		if !known || value == "" {
			continue
		}
		src := filepath.Join(pair.Dir, name)
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if existing, err := os.ReadFile(value); err == nil && bytes.Equal(existing, data) {
			continue
		}
		if err := writeAtomic(value, data, 0600); err != nil {
			return fmt.Errorf("sync %s -> %s: %w", name, value, err)
		}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func localPoolSeqIn(dir string) uint64 {
	raw, err := os.ReadFile(filepath.Join(dir, "material.pool_seq"))
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return v
}

// LocalExpiry returns the installed expires_at, or 0 when nothing is installed.
func (a *Agent) LocalExpiry() uint64 { return localExpiryIn(a.cfg.Dir) }

// LocalRevision returns the installed revision, or 0 when nothing is installed.
func (a *Agent) LocalRevision() uint64 { return localRevisionIn(a.cfg.Dir) }

func localExpiryIn(dir string) uint64 {
	raw, err := os.ReadFile(filepath.Join(dir, "material.expires"))
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return v
}

func localRevisionIn(dir string) uint64 {
	raw, err := os.ReadFile(filepath.Join(dir, "material.revision"))
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return v
}

func (a *Agent) logf(format string, args ...any) {
	logger := a.logger
	if logger == nil {
		logger = log.Default()
	}
	logger.Printf(LogPrefix+format, args...)
}

func runCommand(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restart: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".material-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
