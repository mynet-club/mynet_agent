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
		len(c.RestartCommand) == 0 || len(c.ServerPin) != 64 {
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
}

// Agent is a material client bound to one validated configuration.
type Agent struct {
	cfg     Config
	client  *http.Client
	restart func() error
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
	return &Agent{
		cfg:    cfg,
		client: client,
		logger: log.Default(),
		restart: func() error {
			return runCommand(cfg.RestartCommand)
		},
	}, nil
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
	localRev := a.LocalRevision()
	reqURL := a.cfg.URL + "?transport=" + url.QueryEscape(a.cfg.Transport)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("material server: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var material response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&material); err != nil {
		return err
	}
	if material.Revision == 0 || material.LeaseA == "" || material.LeaseB == "" ||
		material.Revocations == "" || material.FloorName == "" || material.FloorB64 == "" {
		return errors.New("incomplete material response")
	}
	// The issuer rotates before expiry, so a revision already installed is
	// authoritative; re-installing it would restart utcd on every poll.
	if material.Revision <= localRev {
		if a.LocalExpiry() != material.ExpiresAt {
			_ = writeAtomic(filepath.Join(a.cfg.Dir, "material.expires"),
				[]byte(strconv.FormatUint(material.ExpiresAt, 10)+"\n"), 0600)
		}
		return nil
	}
	floor, err := base64.StdEncoding.DecodeString(material.FloorB64)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.cfg.StateDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(a.cfg.StateDir, 0700); err != nil {
		return err
	}
	files := map[string][]byte{
		filepath.Join(a.cfg.Dir, "lease-a.json"):                         []byte(material.LeaseA),
		filepath.Join(a.cfg.Dir, "lease-b.json"):                         []byte(material.LeaseB),
		filepath.Join(a.cfg.Dir, "revocations.json"):                     []byte(material.Revocations),
		filepath.Join(a.cfg.StateDir, filepath.Base(material.FloorName)): floor,
	}
	for path, data := range files {
		if err := writeAtomic(path, data, 0600); err != nil {
			return err
		}
	}
	if err := writeAtomic(filepath.Join(a.cfg.Dir, "material.revision"),
		[]byte(strconv.FormatUint(material.Revision, 10)+"\n"), 0600); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(a.cfg.Dir, "material.expires"),
		[]byte(strconv.FormatUint(material.ExpiresAt, 10)+"\n"), 0600); err != nil {
		return err
	}
	if err := a.restart(); err != nil {
		return err
	}
	a.logf("installed revision=%d expires=%d", material.Revision, material.ExpiresAt)
	return nil
}

// LocalExpiry returns the installed expires_at, or 0 when nothing is installed.
func (a *Agent) LocalExpiry() uint64 {
	raw, err := os.ReadFile(filepath.Join(a.cfg.Dir, "material.expires"))
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return v
}

// LocalRevision returns the installed revision, or 0 when nothing is installed.
func (a *Agent) LocalRevision() uint64 {
	raw, err := os.ReadFile(filepath.Join(a.cfg.Dir, "material.revision"))
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
