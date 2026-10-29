package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// DesktopAccelerationConfig is the deliberately small contract exposed to
// Windows/macOS/Linux desktop clients. Gateway-only ProxyPolicy fields must
// never be required to read or write this object.
type DesktopAccelerationConfig struct {
	// Enabled 加速总开关（钉扎/隧道策略）。
	Enabled bool `json:"enabled"`
	// DNSEnabled DNS 接管开关；**必须 Enabled 才能开**（开 DNS 必开加速）。
	DNSEnabled bool `json:"dns_enabled"`
	// DNSSources 解析器优先级；**有值的源才参与**，顺序即生效链。
	DNSSources []string `json:"dns_sources"`
	// CustomDNS custom 源的解析器值（IP 列表）。
	CustomDNS     []string `json:"custom_dns,omitempty"`
	CustomDomains []string `json:"custom_domains"`
}

// ValidateDNSSplit 加速/DNS 分离约束：开 DNS 必开加速。
func (c *DesktopAccelerationConfig) ValidateDNSSplit() error {
	if c.DNSEnabled && !c.Enabled {
		return fmt.Errorf("DNS 接管必须先开启加速（开 DNS 必开加速）")
	}
	return nil
}

const (
	DesktopDNSCustom      = "custom"
	DesktopDNSTenant      = "tenant"
	DesktopDNSPeer        = "peer"
	DesktopDNSCleanPublic = "clean_public"
)

var desktopDomainPattern = regexp.MustCompile(`^(?:\*\.)?(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// NormalizeDesktopDomains trims, lowercases, de-duplicates and sorts local
// entries. Returning an error for one invalid entry keeps a typo from silently
// changing the effective acceleration set.
func NormalizeDesktopDomains(in []string) ([]string, error) {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		d := strings.ToLower(strings.TrimSpace(raw))
		d = strings.TrimSuffix(d, ".")
		if d == "" {
			continue
		}
		if !desktopDomainPattern.MatchString(d) {
			return nil, fmt.Errorf("invalid desktop domain %q", raw)
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

// DesktopDNSSource is a candidate in priority order. The first healthy source
// is selected; sources are not merged or raced.
type DesktopDNSSource struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Healthy bool   `json:"healthy"`
}

func ChooseDesktopDNSSource(priority []string, candidates map[string]DesktopDNSSource) (DesktopDNSSource, bool) {
	for _, name := range priority {
		if source, ok := candidates[name]; ok && source.Healthy && strings.TrimSpace(source.Address) != "" {
			return source, true
		}
	}
	return DesktopDNSSource{}, false
}
