// Package hubd is the resident UTC Hub agent runtime. It owns the node's
// configured pair/side set, renews each grant before expiry and publishes one
// signed projection to the local GNB as utc.conf. It never falls back to direct
// routing and only records success after GNB reports the matching pair APPLIED
// in utc.status.
package hubd

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/utc/hub"
)

const (
	defaultLeaseTTL      = 3600 * time.Second
	defaultRenewLead     = 600 * time.Second
	defaultJitterMax     = 60 * time.Second
	defaultStatusPoll    = 250 * time.Millisecond
	defaultApplyTimeout  = 15 * time.Second
	defaultRetryMin      = 1 * time.Second
	defaultRetryMax      = 60 * time.Second
	defaultStatusMaxAge  = 5 * time.Second
	defaultClientTimeout = 5 * time.Second
	maxConfigBytes       = 64 << 10
	maxPairs             = 128
)

// PairConfig is one pair/side, its local A-D listener and its durable state.
type PairConfig struct {
	PairID uint64
	PeerID uint64
	Side   uint8
	Mode   uint8
	// LocalEndpoint is the utcd client UDP listener for A-D (mode 2). It must
	// be empty for A-S, whose target comes from the signed TargetBinding.
	LocalEndpoint *hub.LocalEndpoint
	SummaryPath   string
	VerifiedPath  string
}

// Config is the complete local wiring for one node. All paths and the issuer
// key are local installation state; a control request cannot override them.
type Config struct {
	HubAddress      string
	Scope           string
	TransportID     string
	AccountID       string
	IssuerPublicKey ed25519.PublicKey
	StreamID        uint32
	ClientTimeout   time.Duration

	// Identity is the shared utc-v2 header; each pair carries its own PeerID.
	Identity hub.Identity
	Pairs    []PairConfig

	// Bootstrap, when set, replaces a static HubAddress with the Core endpoint
	// from a Core-signed NodeDescriptor; static address and bootstrap cannot
	// be combined.
	Bootstrap *BootstrapConfig

	ConfDir    string
	StatusPath string
	MeterPath  string

	LeaseTTL     time.Duration
	RenewLead    time.Duration
	JitterMax    time.Duration
	StatusPoll   time.Duration
	ApplyTimeout time.Duration
	RetryMin     time.Duration
	RetryMax     time.Duration
	StatusMaxAge time.Duration
}

type fileIdentity struct {
	Scope        string `json:"scope"`
	Instance     string `json:"instance"`
	LocalID      uint64 `json:"local_id"`
	BootID       string `json:"boot_id"`
	PID          uint64 `json:"pid"`
	Capabilities uint32 `json:"capabilities"`
	ConfigGen    uint64 `json:"config_gen"`
}

type filePair struct {
	PairID       uint64 `json:"pair_id"`
	PeerID       uint64 `json:"peer_id"`
	Side         uint8  `json:"side"`
	Mode         uint8  `json:"mode"`
	LocalAddress string `json:"local_address,omitempty"`
	LocalPort    uint16 `json:"local_port,omitempty"`
	SummaryPath  string `json:"summary_path"`
	VerifiedPath string `json:"verified_path,omitempty"`
}

type fileConfig struct {
	Address         string          `json:"address"`
	Scope           string          `json:"scope"`
	TransportID     string          `json:"transport_id"`
	AccountID       string          `json:"account_id"`
	IssuerPublicKey string          `json:"issuer_public_key"`
	StreamID        uint32          `json:"stream_id"`
	ClientTimeout   int64           `json:"client_timeout_seconds"`
	Identity        fileIdentity    `json:"identity"`
	Pairs           []filePair      `json:"pairs"`
	ConfDir         string          `json:"conf_dir"`
	StatusPath      string          `json:"status_path"`
	MeterPath       string          `json:"meter_path,omitempty"`
	Bootstrap       json.RawMessage `json:"bootstrap,omitempty"`
	LeaseTTL        int64           `json:"lease_ttl_seconds"`
	RenewLead       int64           `json:"renew_lead_seconds"`
	JitterMax       int64           `json:"jitter_seconds"`
	StatusPollMS    int64           `json:"status_poll_millis"`
	ApplyTimeout    int64           `json:"apply_timeout_seconds"`
	RetryMin        int64           `json:"retry_min_seconds"`
	RetryMax        int64           `json:"retry_max_seconds"`
	StatusMaxAge    int64           `json:"status_max_age_seconds"`
}

// Load reads a strict JSON configuration file. Any unknown field, trailing
// data, oversized file or world/group-writable file fails closed.
func Load(path string) (Config, error) {
	var config Config
	info, err := os.Lstat(path)
	if err != nil {
		return config, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > maxConfigBytes {
		return config, errors.New("utc hub config must be a bounded, non-writable-by-others file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return config, err
	}
	var file fileConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return config, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return config, errors.New("utc hub config has trailing data")
	}
	key, err := hex.DecodeString(file.IssuerPublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return config, errors.New("utc hub issuer_public_key must be 32-byte hex")
	}
	bootstrap, err := parseBootstrap(file.Bootstrap)
	if err != nil {
		return config, err
	}
	if bootstrap != nil && file.Address != "" {
		return config, errors.New("utc hub static address and bootstrap are mutually exclusive")
	}
	identityScope := file.Identity.Scope
	if identityScope == "" {
		identityScope = file.Scope
	}
	config = Config{
		HubAddress: file.Address, Scope: file.Scope,
		TransportID: file.TransportID, AccountID: file.AccountID,
		IssuerPublicKey: ed25519.PublicKey(key),
		StreamID:        file.StreamID,
		ClientTimeout:   time.Duration(file.ClientTimeout) * time.Second,
		Identity: hub.Identity{
			Scope: identityScope, Instance: file.Identity.Instance,
			LocalID: file.Identity.LocalID,
			BootID:  file.Identity.BootID, PID: file.Identity.PID,
			Capabilities: file.Identity.Capabilities, ConfigGen: file.Identity.ConfigGen,
		},
		ConfDir: file.ConfDir, StatusPath: file.StatusPath,
		MeterPath:    file.MeterPath,
		LeaseTTL:     time.Duration(file.LeaseTTL) * time.Second,
		RenewLead:    time.Duration(file.RenewLead) * time.Second,
		JitterMax:    time.Duration(file.JitterMax) * time.Second,
		StatusPoll:   time.Duration(file.StatusPollMS) * time.Millisecond,
		ApplyTimeout: time.Duration(file.ApplyTimeout) * time.Second,
		RetryMin:     time.Duration(file.RetryMin) * time.Second,
		RetryMax:     time.Duration(file.RetryMax) * time.Second,
		StatusMaxAge: time.Duration(file.StatusMaxAge) * time.Second,
		Bootstrap:    bootstrap,
	}
	for _, item := range file.Pairs {
		pair := PairConfig{
			PairID: item.PairID, PeerID: item.PeerID, Side: item.Side, Mode: item.Mode,
			SummaryPath: item.SummaryPath, VerifiedPath: item.VerifiedPath,
		}
		if item.Mode == hub.PeerModeAD || item.LocalAddress != "" || item.LocalPort != 0 {
			pair.LocalEndpoint = &hub.LocalEndpoint{
				Address: item.LocalAddress, Port: item.LocalPort,
			}
		}
		config.Pairs = append(config.Pairs, pair)
	}
	return config, config.validate()
}

func (c *Config) applyDefaults() {
	if c.StatusPath == "" && c.ConfDir != "" {
		c.StatusPath = filepath.Join(c.ConfDir, "utc.status")
	}
	for i := range c.Pairs {
		if c.Pairs[i].VerifiedPath == "" && c.Pairs[i].SummaryPath != "" {
			c.Pairs[i].VerifiedPath = c.Pairs[i].SummaryPath + ".verified"
		}
	}
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = defaultLeaseTTL
	}
	if c.RenewLead <= 0 {
		c.RenewLead = defaultRenewLead
	}
	if c.JitterMax <= 0 {
		c.JitterMax = defaultJitterMax
	}
	if c.StatusPoll <= 0 {
		c.StatusPoll = defaultStatusPoll
	}
	if c.ApplyTimeout <= 0 {
		c.ApplyTimeout = defaultApplyTimeout
	}
	if c.RetryMin <= 0 {
		c.RetryMin = defaultRetryMin
	}
	if c.RetryMax < c.RetryMin {
		c.RetryMax = defaultRetryMax
	}
	if c.StatusMaxAge <= 0 {
		c.StatusMaxAge = defaultStatusMaxAge
	}
	if c.ClientTimeout <= 0 {
		c.ClientTimeout = defaultClientTimeout
	}
}

func (c *Config) validate() error {
	c.applyDefaults()
	if c.HubAddress == "" && c.Bootstrap == nil {
		return errors.New("utc hub address or bootstrap is required")
	}
	if c.TransportID == "" || c.AccountID == "" {
		return errors.New("utc hub transport/account are required")
	}
	if c.Scope == "" && c.Bootstrap == nil {
		return errors.New("utc hub scope is required without bootstrap")
	}
	if c.Scope != "" && c.Identity.Scope != c.Scope {
		return errors.New("utc hub identity scope does not match the configured scope")
	}
	if len(c.Pairs) == 0 {
		return errors.New("utc hub requires at least one pair")
	}
	if len(c.Pairs) > maxPairs {
		return fmt.Errorf("utc hub supports at most %d pairs", maxPairs)
	}
	seenPair := make(map[uint64]bool, len(c.Pairs))
	seenPeer := make(map[uint64]bool, len(c.Pairs))
	for _, pair := range c.Pairs {
		if pair.PairID == 0 || pair.PeerID == 0 || pair.Side > 1 {
			return errors.New("utc hub pair id/peer id/side are invalid")
		}
		if pair.PeerID == c.Identity.LocalID {
			return errors.New("utc hub peer id must differ from the local id")
		}
		if seenPair[pair.PairID] || seenPeer[pair.PeerID] {
			return errors.New("utc hub pairs must have unique pair and peer ids")
		}
		seenPair[pair.PairID], seenPeer[pair.PeerID] = true, true
		switch pair.Mode {
		case hub.PeerModeAS:
			if pair.LocalEndpoint != nil {
				return errors.New("A-S pair must not carry a local client endpoint")
			}
		case hub.PeerModeAD:
			if pair.LocalEndpoint == nil || pair.LocalEndpoint.Address == "" ||
				pair.LocalEndpoint.Port == 0 {
				return errors.New("A-D pair requires a local client endpoint")
			}
		default:
			return errors.New("utc hub pair mode must be A-S (1) or A-D (2)")
		}
		if pair.SummaryPath == "" {
			return errors.New("utc hub summary_path is required for every pair")
		}
		if pair.VerifiedPath == "" {
			return errors.New("utc hub verified_path could not be derived")
		}
	}
	if c.Identity.Capabilities&hub.CapabilityEpoch == 0 ||
		c.Identity.Capabilities&hub.CapabilityExplicitActivate == 0 ||
		c.Identity.Capabilities&^hub.CapabilityMask != 0 {
		return errors.New("utc hub identity requires epoch and explicit-activate capabilities")
	}
	if c.Identity.ConfigGen == 0 {
		return errors.New("utc hub identity config_gen is required")
	}
	if c.ConfDir == "" {
		return errors.New("utc hub conf_dir is required")
	}
	if c.MeterPath != "" {
		if !filepath.IsAbs(c.MeterPath) {
			return errors.New("utc hub meter_path must be absolute")
		}
		info, err := os.Stat(filepath.Dir(c.MeterPath))
		if err != nil || !info.IsDir() {
			return errors.New("utc hub meter_path parent must be an existing directory")
		}
	}
	if c.RenewLead >= c.LeaseTTL {
		return errors.New("utc hub renew lead must be shorter than the lease TTL")
	}
	// boot_id/pid stay optional: the GNB publishes them to
	// conf_dir/utc.identity at startup and the runtime binds to that file
	// before its first exchange. Everything else must be usable now.
	probe := c.Identity
	probe.PeerID = c.Pairs[0].PeerID
	if probe.BootID == "" {
		probe.BootID = zeroBootID
	}
	if probe.PID == 0 {
		probe.PID = 1
	}
	if err := probe.Validate(); err != nil {
		return fmt.Errorf("utc hub identity: %w", err)
	}
	return nil
}
