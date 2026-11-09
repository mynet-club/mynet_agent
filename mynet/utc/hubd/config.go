// Package hubd is the resident UTC Hub agent runtime. It owns one pair/side,
// renews its grant before expiry and publishes the signed projection to the
// local GNB as utc.conf. It never falls back to direct routing and only records
// success after GNB reports the matching pair APPLIED in utc.status.
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
)

// Config is the complete local wiring for one pair/side. All paths and the
// issuer key are local installation state; a control request cannot override
// them.
type Config struct {
	HubAddress      string
	Scope           string
	TransportID     string
	AccountID       string
	IssuerPublicKey ed25519.PublicKey
	StreamID        uint32
	ClientTimeout   time.Duration

	Identity hub.Identity
	PairID   uint64
	Side     uint8
	Mode     uint8

	ConfDir      string
	StatusPath   string
	SummaryPath  string
	VerifiedPath string

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
	PeerID       uint64 `json:"peer_id"`
	BootID       string `json:"boot_id"`
	PID          uint64 `json:"pid"`
	Capabilities uint32 `json:"capabilities"`
	ConfigGen    uint64 `json:"config_gen"`
}

type fileConfig struct {
	Address         string       `json:"address"`
	Scope           string       `json:"scope"`
	TransportID     string       `json:"transport_id"`
	AccountID       string       `json:"account_id"`
	IssuerPublicKey string       `json:"issuer_public_key"`
	StreamID        uint32       `json:"stream_id"`
	ClientTimeout   int64        `json:"client_timeout_seconds"`
	Identity        fileIdentity `json:"identity"`
	PairID          uint64       `json:"pair_id"`
	Side            uint8        `json:"side"`
	Mode            uint8        `json:"mode"`
	ConfDir         string       `json:"conf_dir"`
	StatusPath      string       `json:"status_path"`
	SummaryPath     string       `json:"summary_path"`
	VerifiedPath    string       `json:"verified_path"`
	LeaseTTL        int64        `json:"lease_ttl_seconds"`
	RenewLead       int64        `json:"renew_lead_seconds"`
	JitterMax       int64        `json:"jitter_seconds"`
	StatusPollMS    int64        `json:"status_poll_millis"`
	ApplyTimeout    int64        `json:"apply_timeout_seconds"`
	RetryMin        int64        `json:"retry_min_seconds"`
	RetryMax        int64        `json:"retry_max_seconds"`
	StatusMaxAge    int64        `json:"status_max_age_seconds"`
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
			LocalID: file.Identity.LocalID, PeerID: file.Identity.PeerID,
			BootID: file.Identity.BootID, PID: file.Identity.PID,
			Capabilities: file.Identity.Capabilities, ConfigGen: file.Identity.ConfigGen,
		},
		PairID: file.PairID, Side: file.Side, Mode: file.Mode,
		ConfDir: file.ConfDir, StatusPath: file.StatusPath,
		SummaryPath: file.SummaryPath, VerifiedPath: file.VerifiedPath,
		LeaseTTL:     time.Duration(file.LeaseTTL) * time.Second,
		RenewLead:    time.Duration(file.RenewLead) * time.Second,
		JitterMax:    time.Duration(file.JitterMax) * time.Second,
		StatusPoll:   time.Duration(file.StatusPollMS) * time.Millisecond,
		ApplyTimeout: time.Duration(file.ApplyTimeout) * time.Second,
		RetryMin:     time.Duration(file.RetryMin) * time.Second,
		RetryMax:     time.Duration(file.RetryMax) * time.Second,
		StatusMaxAge: time.Duration(file.StatusMaxAge) * time.Second,
	}
	return config, config.validate()
}

func (c *Config) applyDefaults() {
	if c.StatusPath == "" && c.ConfDir != "" {
		c.StatusPath = filepath.Join(c.ConfDir, "utc.status")
	}
	if c.VerifiedPath == "" && c.SummaryPath != "" {
		c.VerifiedPath = c.SummaryPath + ".verified"
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
	if c.HubAddress == "" {
		return errors.New("utc hub address is required")
	}
	if c.Scope == "" || c.TransportID == "" || c.AccountID == "" {
		return errors.New("utc hub scope/transport/account are required")
	}
	if c.Identity.Scope != c.Scope {
		return errors.New("utc hub identity scope does not match the configured scope")
	}
	if c.PairID == 0 || c.Side > 1 {
		return errors.New("utc hub pair id/side are invalid")
	}
	// The frozen hub projection builder emits A-S export peers only. Refusing
	// A-D here is explicit; it must not be misreported as a supported mode.
	if c.Mode != 1 {
		return errors.New("utc hub runtime currently supports A-S (mode 1) only")
	}
	if c.Identity.Capabilities&hub.CapabilityEpoch == 0 ||
		c.Identity.Capabilities&hub.CapabilityExplicitActivate == 0 ||
		c.Identity.Capabilities&^hub.CapabilityMask != 0 {
		return errors.New("utc hub identity requires epoch and explicit-activate capabilities")
	}
	if c.Identity.ConfigGen == 0 {
		return errors.New("utc hub identity config_gen is required")
	}
	if c.ConfDir == "" || c.SummaryPath == "" {
		return errors.New("utc hub conf_dir and summary_path are required")
	}
	if c.VerifiedPath == "" {
		return errors.New("utc hub verified_path could not be derived")
	}
	if c.RenewLead >= c.LeaseTTL {
		return errors.New("utc hub renew lead must be shorter than the lease TTL")
	}
	// boot_id/pid stay optional: the GNB publishes them to
	// conf_dir/utc.identity at startup and the runtime binds to that file
	// before its first exchange. Everything else must be usable now.
	probe := c.Identity
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
