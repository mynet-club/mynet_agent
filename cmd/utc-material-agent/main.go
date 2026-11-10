// utc-material-agent pulls short-lived UTC relay material from utc-materiald,
// installs it atomically and restarts the local utcd.
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

type config struct {
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

type response struct {
	Revision    uint64 `json:"revision"`
	ExpiresAt   uint64 `json:"expires_at"`
	LeaseA      string `json:"lease_a"`
	LeaseB      string `json:"lease_b"`
	Revocations string `json:"revocations"`
	FloorName   string `json:"floor_name"`
	FloorB64    string `json:"floor_b64"`
}

func main() {
	path := flag.String("c", "", "config path")
	once := flag.Bool("once", false, "fetch once and exit")
	flag.Parse()
	if *path == "" {
		log.Fatal("utc-material-agent: -c <config> is required")
	}
	raw, err := os.ReadFile(*path)
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
	client, err := cfg.client()
	if err != nil {
		log.Fatal(err)
	}
	a := &agent{cfg: cfg, client: client}
	if err := a.sync(); err != nil {
		log.Printf("utc-material-agent: initial sync failed: %v", err)
	}
	if *once {
		return
	}
	basePoll := time.Duration(cfg.PollSeconds) * time.Second
	fastPoll := 30 * time.Second
	for {
		wait := basePoll
		if exp := a.localExpiry(); exp > 0 &&
			exp <= uint64(time.Now().Unix())+uint64(cfg.RefreshBeforeSecs) && wait > fastPoll {
			wait = fastPoll
		}
		time.Sleep(wait)
		if err := a.sync(); err != nil {
			log.Printf("utc-material-agent: sync failed: %v", err)
		}
	}
}

func (c *config) validate() error {
	if c.URL == "" || c.CA == "" || c.Certificate == "" || c.PrivateKey == "" ||
		c.ServerPin == "" || c.Dir == "" || c.StateDir == "" || c.Transport == "" ||
		len(c.RestartCommand) == 0 || len(c.ServerPin) != 64 {
		return errors.New("utc-material-agent: config is incomplete")
	}
	if c.PollSeconds <= 0 {
		c.PollSeconds = 300
	}
	if c.RefreshBeforeSecs <= 0 {
		c.RefreshBeforeSecs = 1200
	}
	return nil
}

func (c *config) client() (*http.Client, error) {
	cert, err := tls.LoadX509KeyPair(c.Certificate, c.PrivateKey)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pem, err := os.ReadFile(c.CA)
	if err != nil || !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("cannot load CA")
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
			if hex.EncodeToString(sum[:]) != c.ServerPin {
				return errors.New("server SPKI pin mismatch")
			}
			return nil
		},
	}
	transport := &http.Transport{TLSClientConfig: tlsCfg}
	return &http.Client{Transport: transport, Timeout: 20 * time.Second}, nil
}

type agent struct {
	cfg    config
	client *http.Client
}

func (a *agent) sync() error {
	localRev := a.localRevision()
	reqURL := a.cfg.URL + "?transport=" + url.QueryEscape(a.cfg.Transport)
	resp, err := a.client.Get(reqURL)
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
		if a.localExpiry() != material.ExpiresAt {
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
	log.Printf("utc-material-agent: installed revision=%d expires=%d", material.Revision, material.ExpiresAt)
	return nil
}

func (a *agent) localExpiry() uint64 {
	raw, err := os.ReadFile(filepath.Join(a.cfg.Dir, "material.expires"))
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return v
}

func (a *agent) localRevision() uint64 {
	raw, err := os.ReadFile(filepath.Join(a.cfg.Dir, "material.revision"))
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return v
}

func (a *agent) restart() error {
	cmd := exec.Command(a.cfg.RestartCommand[0], a.cfg.RestartCommand[1:]...)
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
