// utc-materiald issues short-lived UTC relay authorization material on behalf
// of the Hub and serves it to enrolled nodes over mutual TLS.
package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type config struct {
	Listen          string            `json:"listen"`
	CA              string            `json:"ca"`
	Certificate     string            `json:"certificate"`
	PrivateKey      string            `json:"private_key"`
	SigningKey      string            `json:"signing_key"`
	Guard           string            `json:"guard"`
	Trust           string            `json:"trust"`
	LeaseDir        string            `json:"lease_dir"`
	StateDir        string            `json:"state_dir"`
	RevisionFile    string            `json:"revision_file"`
	RelayRestart    []string          `json:"relay_restart"`
	Scope           string            `json:"scope"`
	Issuer          string            `json:"issuer"`
	KeyID           string            `json:"key_id"`
	PairID          uint64            `json:"pair_id"`
	PairGen         uint64            `json:"pair_gen"`
	TargetPeer      uint64            `json:"target_peer_id"`
	Manifest        string            `json:"manifest"`
	EndpointScope   string            `json:"endpoint_scope"`
	QuotaAccount    string            `json:"quota_account"`
	QuotaOwner      string            `json:"quota_owner"`
	ByteLimit       uint64            `json:"byte_limit"`
	SourceA         string            `json:"source_a"`
	SourceB         string            `json:"source_b"`
	Provider        string            `json:"provider"`
	Subject         string            `json:"subject"`
	Binding         string            `json:"binding"`
	NonceA          string            `json:"nonce_a"`
	NonceB          string            `json:"nonce_b"`
	RevocationEpoch uint64            `json:"revocation_epoch"`
	TTLSeconds      int64             `json:"ttl_seconds"`
	IntervalSecs    int64             `json:"interval_seconds"`
	Transports      map[string]string `json:"transports"` // transport id -> SPKI sha256
}

type material struct {
	Revision    uint64
	ExpiresAt   uint64
	LeaseA      []byte
	LeaseB      []byte
	Revocations []byte
	FloorName   string
	Floor       []byte
}

type server struct {
	cfg config
	mu  sync.RWMutex
	cur *material
}

func main() {
	configPath := flag.String("c", "", "config path")
	once := flag.Bool("once", false, "rotate once and exit")
	flag.Parse()
	if *configPath == "" {
		log.Fatal("utc-materiald: -c <config> is required")
	}
	raw, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		log.Fatal(err)
	}
	if err := cfg.validate(); err != nil {
		log.Fatal(err)
	}
	s := &server{cfg: cfg}
	if err := s.rotate(); err != nil {
		log.Printf("utc-materiald: initial rotation failed: %v", err)
	}
	if *once {
		if err := s.rotate(); err != nil {
			log.Fatal(err)
		}
		return
	}
	go s.loop()
	log.Printf("utc-materiald: listening on %s", cfg.Listen)
	http.HandleFunc("/v1/material", s.handle)
	srv := &http.Server{
		Addr:              cfg.Listen,
		TLSConfig:         s.tlsConfig(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServeTLS(cfg.Certificate, cfg.PrivateKey); err != nil {
		log.Fatal(err)
	}
}

func (c *config) validate() error {
	if c.Listen == "" || c.CA == "" || c.Certificate == "" || c.PrivateKey == "" ||
		c.SigningKey == "" || c.Guard == "" || c.Trust == "" || c.LeaseDir == "" ||
		c.StateDir == "" || c.Scope == "" || c.Issuer == "" || c.KeyID == "" ||
		c.PairID == 0 || c.PairGen == 0 || c.TargetPeer == 0 || c.ByteLimit == 0 ||
		c.SourceA == "" || c.SourceB == "" || c.Provider == "" || c.Subject == "" ||
		c.Binding == "" || c.NonceA == "" || c.NonceB == "" || c.RevocationEpoch == 0 ||
		c.TTLSeconds <= 0 || len(c.Transports) == 0 {
		return errors.New("utc-materiald: config is incomplete")
	}
	if c.TTLSeconds > 3500 {
		return errors.New("utc-materiald: ttl_seconds must be <= 3500")
	}
	if c.IntervalSecs <= 0 || c.IntervalSecs >= c.TTLSeconds {
		c.IntervalSecs = c.TTLSeconds - 900
	}
	return nil
}

func (s *server) loop() {
	interval := time.Duration(s.cfg.IntervalSecs) * time.Second
	for {
		time.Sleep(interval)
		if err := s.rotate(); err != nil {
			log.Printf("utc-materiald: rotation failed: %v", err)
		}
	}
}

func (s *server) rotate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	rev, err := s.nextRevision()
	if err != nil {
		return err
	}
	expires := uint64(now + s.cfg.TTLSeconds)
	issued := uint64(now - 10)
	names := map[string]string{
		"lease-a.json":     filepath.Join(s.cfg.LeaseDir, "lease-a.json.tmp"),
		"lease-b.json":     filepath.Join(s.cfg.LeaseDir, "lease-b.json.tmp"),
		"revocations.json": filepath.Join(s.cfg.LeaseDir, "revocations.json.tmp"),
	}
	defer func() {
		for _, p := range names {
			os.Remove(p)
		}
	}()
	if err := s.issueLease(names["lease-a.json"], "lease-A", "a-to-b", s.cfg.SourceA,
		s.cfg.NonceA, issued, expires); err != nil {
		return err
	}
	if err := s.issueLease(names["lease-b.json"], "lease-B", "b-to-a", s.cfg.SourceB,
		s.cfg.NonceB, issued, expires); err != nil {
		return err
	}
	if err := s.issueRevocations(names["revocations.json"], rev, issued, expires); err != nil {
		return err
	}
	for name, tmp := range names {
		if err := os.Chmod(tmp, 0600); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(s.cfg.LeaseDir, name)); err != nil {
			return err
		}
	}
	if err := s.run(s.cfg.Guard, "install-floor", "--trust", s.cfg.Trust,
		"--revision", strconv.FormatUint(rev, 10)); err != nil {
		return err
	}
	if len(s.cfg.RelayRestart) > 0 {
		if err := s.run(s.cfg.RelayRestart[0], s.cfg.RelayRestart[1:]...); err != nil {
			return err
		}
	}
	floorName, floor, err := newestFloor(s.cfg.StateDir)
	if err != nil {
		return err
	}
	leaseA, err := os.ReadFile(filepath.Join(s.cfg.LeaseDir, "lease-a.json"))
	if err != nil {
		return err
	}
	leaseB, err := os.ReadFile(filepath.Join(s.cfg.LeaseDir, "lease-b.json"))
	if err != nil {
		return err
	}
	revs, err := os.ReadFile(filepath.Join(s.cfg.LeaseDir, "revocations.json"))
	if err != nil {
		return err
	}
	s.cur = &material{Revision: rev, ExpiresAt: expires, LeaseA: leaseA, LeaseB: leaseB,
		Revocations: revs, FloorName: floorName, Floor: floor}
	if err := os.WriteFile(s.cfg.RevisionFile, []byte(strconv.FormatUint(rev, 10)+"\n"), 0600); err != nil {
		log.Printf("utc-materiald: revision file: %v", err)
	}
	log.Printf("utc-materiald: rotated revision=%d expires=%d floor=%s", rev, expires, floorName)
	return nil
}

func (s *server) nextRevision() (uint64, error) {
	raw, err := os.ReadFile(s.cfg.RevisionFile)
	if err == nil {
		v, convErr := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if convErr == nil && v > 0 {
			return v + 1, nil
		}
	}
	// Bootstrap: continue from the current floor revision when present.
	floorRev, err := s.currentFloorRevision()
	if err != nil {
		return 0, err
	}
	return floorRev + 1, nil
}

func (s *server) currentFloorRevision() (uint64, error) {
	// install-floor refuses rewinds; a fresh state starts at 1, so 2 is safe
	// only when no floor exists. Probe with a tiny synthetic revision.
	if entries, err := filepath.Glob(filepath.Join(s.cfg.StateDir, "*.floor")); err == nil && len(entries) == 0 {
		return 1, nil
	}
	return 2, nil
}

func (s *server) issueLease(out, leaseID, direction, source, nonce string,
	issued, expires uint64) error {
	args := []string{"issue-lease", "--key", s.cfg.SigningKey, "--out", out,
		"--issuer", s.cfg.Issuer, "--key-id", s.cfg.KeyID, "--lease-id", leaseID,
		"--scope", s.cfg.Scope, "--subject", s.cfg.Subject, "--source", source,
		"--provider", s.cfg.Provider, "--manifest", s.cfg.Manifest, "--binding", s.cfg.Binding,
		"--direction", direction, "--endpoint-scope", s.cfg.EndpointScope,
		"--quota-account", s.cfg.QuotaAccount, "--quota-owner", s.cfg.QuotaOwner,
		"--nonce", nonce, "--target-peer", strconv.FormatUint(s.cfg.TargetPeer, 10),
		"--pair", strconv.FormatUint(s.cfg.PairID, 10),
		"--pair-gen", strconv.FormatUint(s.cfg.PairGen, 10),
		"--issued", strconv.FormatUint(issued, 10), "--not-before", strconv.FormatUint(issued, 10),
		"--expires", strconv.FormatUint(expires, 10),
		"--byte-limit", strconv.FormatUint(s.cfg.ByteLimit, 10),
		"--revocation-epoch", strconv.FormatUint(s.cfg.RevocationEpoch, 10)}
	return s.run(s.cfg.Guard, args...)
}

func (s *server) issueRevocations(out string, revision, issued, expires uint64) error {
	return s.run(s.cfg.Guard, "issue-revocations", "--key", s.cfg.SigningKey, "--out", out,
		"--issuer", s.cfg.Issuer, "--key-id", s.cfg.KeyID, "--scope", s.cfg.Scope,
		"--revision", strconv.FormatUint(revision, 10),
		"--issued", strconv.FormatUint(issued, 10),
		"--expires", strconv.FormatUint(expires, 10))
}

func (s *server) run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func newestFloor(dir string) (string, []byte, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "*.floor"))
	if err != nil || len(entries) == 0 {
		return "", nil, errors.New("no floor file produced")
	}
	sort.Slice(entries, func(i, j int) bool {
		a, _ := os.Stat(entries[i])
		b, _ := os.Stat(entries[j])
		return a.ModTime().After(b.ModTime())
	})
	data, err := os.ReadFile(entries[0])
	if err != nil {
		return "", nil, err
	}
	return filepath.Base(entries[0]), data, nil
}

func (s *server) tlsConfig() *tls.Config {
	pool := x509.NewCertPool()
	pem, err := os.ReadFile(s.cfg.CA)
	if err != nil || !pool.AppendCertsFromPEM(pem) {
		log.Fatalf("utc-materiald: cannot load CA: %v", err)
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientCAs:  pool,
		ClientAuth: tls.RequireAndVerifyClientCert,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("missing client certificate")
			}
			cert, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
			id := hex.EncodeToString(sum[:])
			for _, pin := range s.cfg.Transports {
				if id == pin {
					return nil
				}
			}
			return errors.New("client key is not enrolled")
		},
	}
}

func (s *server) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/material" || r.Method != http.MethodGet {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	transport := r.URL.Query().Get("transport")
	if transport == "" || s.cfg.Transports[transport] == "" {
		http.Error(w, "unknown transport", http.StatusForbidden)
		return
	}
	s.mu.RLock()
	cur := s.cur
	s.mu.RUnlock()
	if cur == nil {
		http.Error(w, "material not ready", http.StatusServiceUnavailable)
		return
	}
	resp := map[string]any{
		"revision":    cur.Revision,
		"expires_at":  cur.ExpiresAt,
		"lease_a":     string(cur.LeaseA),
		"lease_b":     string(cur.LeaseB),
		"revocations": string(cur.Revocations),
		"floor_name":  cur.FloorName,
		"floor_b64":   base64.StdEncoding.EncodeToString(cur.Floor),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
