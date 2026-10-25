package mynet

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type dnsBlockBaseline struct {
	Ads     []string `json:"ads"`
	Malware []string `json:"malware"`
	Version string   `json:"version"`
}

func dnsBlockBaselinePath() string { return filepath.Join(ProxyStateDir(), "dns_block_baseline.json") }

func (b *dnsBlockBaseline) digest() string {
	raw, _ := json.Marshal([][]string{b.Ads, b.Malware})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func loadDNSBlockBaseline() (*dnsBlockBaseline, error) {
	raw, err := os.ReadFile(dnsBlockBaselinePath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var b dnsBlockBaseline
	if err = json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	if b.Version == "" || b.Version != b.digest() {
		return nil, fmt.Errorf("invalid DNS block baseline version")
	}
	for _, list := range [][]string{b.Ads, b.Malware} {
		for _, d := range list {
			n, e := blockDomain(d)
			if e != nil || n != d {
				return nil, fmt.Errorf("invalid DNS block baseline domain %q", d)
			}
		}
	}
	return &b, nil
}

// Both lists are one transaction. An empty, successfully fetched pair is authoritative.
func refreshDNSBlockBaseline(ctx context.Context, client *http.Client, base string) error {
	b := &dnsBlockBaseline{}
	for _, src := range []struct {
		name string
		dest *[]string
	}{{"ad-domains.txt", &b.Ads}, {"malware-domains.txt", &b.Malware}} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+src.name, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("ctl %s: HTTP %d", src.name, resp.StatusCode)
		}
		if readErr != nil {
			return readErr
		}
		if len(raw) > 1024*1024 {
			return fmt.Errorf("ctl %s exceeds 1 MiB", src.name)
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
			if line == "" {
				continue
			}
			// Plain hostnames only: ads use suffix semantics; malware hosts stay exact.
			d, err := blockDomain(line)
			if err != nil || strings.HasPrefix(line, ".") || strings.HasPrefix(line, "*") {
				return fmt.Errorf("ctl %s invalid domain %q", src.name, line)
			}
			if !seen[d] {
				*src.dest = append(*src.dest, d)
				seen[d] = true
			}
		}
	}
	b.Version = b.digest()
	raw, _ := json.Marshal(b)
	if err := os.MkdirAll(ProxyStateDir(), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(ProxyStateDir(), ".dns-block-baseline-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, dnsBlockBaselinePath())
}
