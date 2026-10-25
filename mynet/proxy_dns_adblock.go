package mynet

import (
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed dnsblock/ads.txt
var builtinAdDomains string

//go:embed dnsblock/urlhaus.txt
var builtinMalwareHosts string

// Missing enabled means enabled. Allow entries are explicit overrides, including subdomains.
type dnsAdBlockConfig struct {
	Enabled *bool    `json:"enabled,omitempty"`
	Block   []string `json:"block,omitempty"`
	Allow   []string `json:"allow,omitempty"`
}

type dnsAdBlock struct {
	source               string
	version              string
	enabled              bool
	suffix, exact, allow map[string]bool
}

var defaultDNSBlockOnce sync.Once
var defaultDNSBlock *dnsAdBlock

func dnsAdBlockPath() string { return filepath.Join(ProxyStateDir(), "dns_adblock.json") }

func blockDomain(s string) (string, error) {
	s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	s = strings.TrimPrefix(strings.TrimPrefix(s, "*."), ".")
	if len(s) > 253 || net.ParseIP(s) != nil || !strings.Contains(s, ".") {
		return "", fmt.Errorf("invalid block domain %q", s)
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid block domain %q", s)
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("invalid block domain %q", s)
			}
		}
	}
	return s, nil
}

func builtinDNSBlock() *dnsAdBlock {
	defaultDNSBlockOnce.Do(func() {
		defaultDNSBlock = &dnsAdBlock{enabled: true, suffix: map[string]bool{}, exact: map[string]bool{}, allow: map[string]bool{}}
		for _, src := range []struct {
			raw  string
			dest map[string]bool
		}{{builtinAdDomains, defaultDNSBlock.suffix}, {builtinMalwareHosts, defaultDNSBlock.exact}} {
			for _, line := range strings.Split(src.raw, "\n") {
				line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
				if line == "" {
					continue
				}
				d, err := blockDomain(line)
				if err != nil {
					panic(err)
				}
				src.dest[d] = true
			}
		}
	})
	return defaultDNSBlock
}

func readDNSAdBlockConfig() (dnsAdBlockConfig, error) {
	var c dnsAdBlockConfig
	raw, err := os.ReadFile(dnsAdBlockPath())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if len(raw) > 1024*1024 {
		return c, fmt.Errorf("DNS block config exceeds 1 MiB")
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	for _, list := range [][]string{c.Block, c.Allow} {
		for _, s := range list {
			if _, err := blockDomain(s); err != nil {
				return c, err
			}
		}
	}
	return c, nil
}

func loadDNSAdBlock() (*dnsAdBlock, string, error) {
	c, err := readDNSAdBlockConfig()
	if err != nil {
		return nil, "", err
	}
	b := builtinDNSBlock()
	source, version := "builtin", "builtin-v1"
	baseline, err := loadDNSBlockBaseline()
	if err != nil {
		return nil, "", err
	}
	if baseline != nil {
		b = &dnsAdBlock{suffix: map[string]bool{}, exact: map[string]bool{}}
		for _, d := range baseline.Ads {
			b.suffix[d] = true
		}
		for _, d := range baseline.Malware {
			b.exact[d] = true
		}
		source, version = "ctl", baseline.Version
	}
	r := &dnsAdBlock{source: source, version: version, enabled: c.Enabled == nil || *c.Enabled, suffix: map[string]bool{}, exact: b.exact, allow: map[string]bool{}}
	for d := range b.suffix {
		r.suffix[d] = true
	}
	for _, d := range c.Block {
		d, _ = blockDomain(d)
		r.suffix[d] = true
	}
	for _, d := range c.Allow {
		d, _ = blockDomain(d)
		r.allow[d] = true
	}
	raw, _ := json.Marshal(c)
	return r, string(raw) + "|" + version, nil
}

// O(label count), independent of list size; immutable maps shared by concurrent readers.
func suffixSetContains(name string, set map[string]bool) bool {
	for {
		if set[name] {
			return true
		}
		i := strings.IndexByte(name, '.')
		if i < 0 {
			return false
		}
		name = name[i+1:]
	}
}

func (b *dnsAdBlock) blocked(name string) bool {
	if b == nil || !b.enabled {
		return false
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	return !suffixSetContains(name, b.allow) && (b.exact[name] || suffixSetContains(name, b.suffix))
}

// Inspect CNAMEs before caching/pinning, including cached upstream responses.
func (b *dnsAdBlock) blockedAlias(msg []byte) bool {
	if b == nil || !b.enabled || len(msg) < 12 {
		return false
	}
	off := 12
	for i := 0; i < int(binary.BigEndian.Uint16(msg[4:6])); i++ {
		_, n, e := parseDNSQName(msg, off)
		if e != nil || n+4 > len(msg) {
			return false
		}
		off = n + 4
	}
	for i := 0; i < int(binary.BigEndian.Uint16(msg[6:8])); i++ {
		_, n, e := parseDNSQName(msg, off)
		if e != nil || n+10 > len(msg) {
			return false
		}
		size := int(binary.BigEndian.Uint16(msg[n+8 : n+10]))
		end := n + 10 + size
		if end > len(msg) {
			return false
		}
		if binary.BigEndian.Uint16(msg[n:n+2]) == 5 {
			name, next, e := parseDNSQName(msg, n+10)
			if e == nil && next == end && b.blocked(name) {
				return true
			}
		}
		off = end
	}
	return false
}

// Synthesize a single-question IN reply; discard EDNS/extra records and clear AD.
func dnsBlockedAnswer(query []byte) []byte {
	if len(query) < 12 || binary.BigEndian.Uint16(query[4:6]) != 1 || query[2]&0xf8 != 0 {
		return dnsFailure(query)
	}
	name, off, err := parseDNSQName(query, 12)
	if err != nil || off+4 > len(query) {
		return nil
	}
	out := append([]byte{}, query[:12]...)
	out[2] = 0x80 | (query[2] & 1)
	out[3] = 0x80 | (query[3] & 0x10)
	for i := 6; i < 12; i++ {
		out[i] = 0
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	out = append(out, 0)
	out = append(out, query[off:off+4]...)
	typ := binary.BigEndian.Uint16(query[off : off+2])
	class := binary.BigEndian.Uint16(query[off+2 : off+4])
	var ip []byte
	if class == 1 {
		switch typ {
		case 1:
			ip = []byte{127, 0, 0, 1}
		case 28:
			ip = net.IPv6loopback.To16()
		}
	}
	if ip != nil {
		binary.BigEndian.PutUint16(out[6:8], 1)
		out = append(out, 0xc0, 0x0c, byte(typ>>8), byte(typ), 0, 1, 0, 0, 0, 60, 0, byte(len(ip)))
		out = append(out, ip...)
	}
	return out
}

func cmdDNSAdBlock(args []string) int {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off" && args[0] != "status" && args[0] != "refresh") {
		fmt.Fprintln(os.Stderr, "用法: mynet proxy dns-hook ad-block on|off|status|refresh")
		return 2
	}
	c, err := readDNSAdBlockConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if args[0] == "refresh" {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := refreshDNSBlockBaseline(ctx, &http.Client{Timeout: 10 * time.Second}, proxyDomainBaselineURL()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if args[0] == "on" || args[0] == "off" {
		enabled := args[0] == "on"
		c.Enabled = &enabled
		raw, _ := json.MarshalIndent(c, "", "  ")
		if err = os.MkdirAll(ProxyStateDir(), 0700); err == nil {
			var f *os.File
			f, err = os.CreateTemp(ProxyStateDir(), ".dns-adblock-*")
			if err == nil {
				tmp := f.Name()
				defer os.Remove(tmp)
				_, err = f.Write(raw)
				closeErr := f.Close()
				if err == nil {
					err = closeErr
				}
				if err == nil {
					err = os.Rename(tmp, dnsAdBlockPath())
				}
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	b, _, err := loadDNSAdBlock()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("ad-block: enabled=%v domains=%d allow=%d source=%s version=%s ttl=60 refresh_seconds=10 hook_required=true\n", b.enabled, len(b.suffix)+len(b.exact), len(b.allow), b.source, b.version)
	return 0
}
