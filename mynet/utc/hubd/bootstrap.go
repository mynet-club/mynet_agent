package hubd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/utc/nodedesc"
)

// BootstrapConfig is the optional Gateway bootstrap block in hub.json. When it
// is present the static "address" must stay empty: the Core control endpoint
// comes only from a Core-signed NodeDescriptor delivered by the Gateway, with
// a bounded, verified cache as the only fallback.
type BootstrapConfig struct {
	URL                 string `json:"url"`
	CachePath           string `json:"cache_path"`
	MaxStaleMS          uint64 `json:"max_stale_ms"`
	CA                  string `json:"ca"`
	Certificate         string `json:"certificate"`
	PrivateKey          string `json:"private_key"`
	ServerName          string `json:"server_name"`
	ServerPin           string `json:"server_pin"`
	DescriptorPublicKey string `json:"descriptor_public_key"`
	IssuerID            string `json:"descriptor_issuer"`
	KeyID               string `json:"descriptor_key"`
	Scope               string `json:"descriptor_scope"`
	Pin                 string `json:"descriptor_pin"`
}

type fileBootstrap struct {
	URL                 string `json:"url"`
	CachePath           string `json:"cache_path"`
	MaxStaleMS          uint64 `json:"max_stale_ms"`
	CA                  string `json:"ca"`
	Certificate         string `json:"certificate"`
	PrivateKey          string `json:"private_key"`
	ServerName          string `json:"server_name"`
	ServerPin           string `json:"server_pin"`
	DescriptorPublicKey string `json:"descriptor_public_key"`
	IssuerID            string `json:"descriptor_issuer"`
	KeyID               string `json:"descriptor_key"`
	Scope               string `json:"descriptor_scope"`
	Pin                 string `json:"descriptor_pin"`
}

func (b *BootstrapConfig) validate() error {
	if b.URL == "" || b.CachePath == "" || b.IssuerID == "" || b.KeyID == "" ||
		b.Scope == "" || b.DescriptorPublicKey == "" || b.Pin == "" {
		return errors.New("utc hub bootstrap config is incomplete")
	}
	if len(b.ServerPin) != 0 && len(b.ServerPin) != 64 {
		return errors.New("utc hub bootstrap server_pin must be 64 hex characters")
	}
	if _, err := hex.DecodeString(b.DescriptorPublicKey); err != nil {
		return errors.New("utc hub bootstrap descriptor_public_key must be hex")
	}
	if _, err := hex.DecodeString(b.Pin); err != nil {
		return errors.New("utc hub bootstrap descriptor_pin must be hex")
	}
	return nil
}

func (b *BootstrapConfig) decode() (ed25519.PublicKey, [32]byte, [32]byte, error) {
	var pin [32]byte
	var gatewayPin [32]byte
	publicKey, err := hex.DecodeString(b.DescriptorPublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return nil, pin, gatewayPin, errors.New("utc hub bootstrap descriptor_public_key is invalid")
	}
	pinBytes, err := hex.DecodeString(b.Pin)
	if err != nil || len(pinBytes) != len(pin) {
		return nil, pin, gatewayPin, errors.New("utc hub bootstrap descriptor_pin is invalid")
	}
	copy(pin[:], pinBytes)
	if b.ServerPin != "" {
		gatewayBytes, err := hex.DecodeString(b.ServerPin)
		if err != nil || len(gatewayBytes) != len(gatewayPin) {
			return nil, pin, gatewayPin, errors.New("utc hub bootstrap server_pin is invalid")
		}
		copy(gatewayPin[:], gatewayBytes)
	}
	return ed25519.PublicKey(publicKey), pin, gatewayPin, nil
}

func (b *BootstrapConfig) httpClient() (*http.Client, error) {
	caBytes, err := os.ReadFile(b.CA)
	if err != nil {
		return nil, fmt.Errorf("utc hub bootstrap ca: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(b.Certificate, b.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("utc hub bootstrap client cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBytes) {
		return nil, errors.New("utc hub bootstrap ca has no certificates")
	}
	_, _, gatewayPin, err := b.decode()
	if err != nil {
		return nil, err
	}
	configuration := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
		ServerName:   b.ServerName,
	}
	if len(b.ServerPin) != 0 {
		configuration.InsecureSkipVerify = true
		configuration.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("utc hub bootstrap peer has no certificate")
			}
			parsed, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			der, err := x509.MarshalPKIXPublicKey(parsed.PublicKey)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(der)
			if digest != gatewayPin {
				return errors.New("utc hub bootstrap server pin mismatch")
			}
			return nil
		}
	}
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: configuration,
		},
	}, nil
}

// Bootstrap resolves the Core CONTROL endpoint from the Gateway and mutates
// config. A static address is mutually exclusive with bootstrap.
func Bootstrap(ctx context.Context, config *Config) error {
	if config == nil || config.Bootstrap == nil {
		return errors.New("utc hub bootstrap: no bootstrap config")
	}
	if config.HubAddress != "" {
		return errors.New("utc hub bootstrap: static address and bootstrap are mutually exclusive")
	}
	client, err := config.Bootstrap.httpClient()
	if err != nil {
		return err
	}
	return bootstrapWithClient(ctx, config, client)
}

func bootstrapWithClient(ctx context.Context, config *Config, client *http.Client) error {
	source := config.Bootstrap
	publicKey, pin, _, err := source.decode()
	if err != nil {
		return err
	}
	nowMS := uint64(time.Now().UnixMilli())
	expect := nodedesc.Expect{
		IssuerID:  source.IssuerID,
		KeyID:     source.KeyID,
		Scope:     source.Scope,
		Pin:       pin,
		NowMS:     nowMS,
		MaxSkewMS: 60000,
	}
	resolver := nodedesc.Resolver{
		URL:        source.URL,
		CachePath:  source.CachePath,
		MaxStaleMS: source.MaxStaleMS,
	}
	descriptor, _, err := resolver.Resolve(ctx, client, expect, publicKey, nowMS)
	if err != nil {
		return err
	}
	config.HubAddress = net.JoinHostPort(descriptor.EndpointHost,
		strconv.FormatUint(uint64(descriptor.Port), 10))
	if config.Scope == "" {
		config.Scope = descriptor.Scope
	}
	if config.Identity.Scope == "" {
		config.Identity.Scope = config.Scope
	}
	return nil
}

func parseBootstrap(raw json.RawMessage) (*BootstrapConfig, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var file fileBootstrap
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, err
	}
	bootstrap := &BootstrapConfig{
		URL: file.URL, CachePath: file.CachePath, MaxStaleMS: file.MaxStaleMS,
		CA: file.CA, Certificate: file.Certificate, PrivateKey: file.PrivateKey,
		ServerName: file.ServerName, ServerPin: file.ServerPin,
		DescriptorPublicKey: file.DescriptorPublicKey,
		IssuerID:            file.IssuerID, KeyID: file.KeyID, Scope: file.Scope,
		Pin: file.Pin,
	}
	if err := bootstrap.validate(); err != nil {
		return nil, err
	}
	return bootstrap, nil
}
