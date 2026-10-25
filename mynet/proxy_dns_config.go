package mynet

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

// Device-local preference, deliberately separate from cloud-synchronized proxy intent.
type hookDNSConfig struct {
	CleanDNS []string `json:"clean_dns,omitempty"`
}

type hookDNSSelection struct {
	Configured []string `json:"configured"`
	Tenant     string   `json:"tenant,omitempty"`
	Source     string   `json:"source"`
	Upstreams  []string `json:"upstreams"`
}

func hookDNSConfigPath() string { return filepath.Join(ProxyStateDir(), "dns_hook.json") }

func validateHookDNSEndpoint(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if ip := net.ParseIP(s); ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
			return "", fmt.Errorf("DNS cannot point to loopback/unspecified/multicast: %q", s)
		}
		return ip.String(), nil
	}
	d, err := blockDomain(s)
	if err != nil || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "*") {
		return "", fmt.Errorf("DNS must be an IP or hostname without URL, port or wildcard: %q", raw)
	}
	return d, nil
}

func loadHookDNSConfig() (hookDNSConfig, error) {
	var c hookDNSConfig
	raw, err := os.ReadFile(hookDNSConfigPath())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if len(raw) > 16384 {
		return c, fmt.Errorf("DNS config exceeds 16 KiB")
	}
	if err = json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	if len(c.CleanDNS) > 8 {
		return c, fmt.Errorf("at most 8 custom DNS endpoints")
	}
	for i, s := range c.CleanDNS {
		d, e := validateHookDNSEndpoint(s)
		if e != nil {
			return c, e
		}
		c.CleanDNS[i] = d
	}
	return c, nil
}

func saveHookDNSConfig(c hookDNSConfig) error {
	if err := os.MkdirAll(ProxyStateDir(), 0700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(c, "", "  ")
	f, err := os.CreateTemp(ProxyStateDir(), ".dns-hook-config-*")
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
	return os.Rename(tmp, hookDNSConfigPath())
}

func configuredHookTenantDNS() string {
	s := &config.Store{}
	_, mode, err := s.ActiveSource()
	if err != nil || mode == nil || mode.Mode != domain.ModeOnline || mode.Active.TenantID == "" {
		return ""
	}
	p, err := s.LoadOnlineProfile(mode.Active.TenantID)
	if err != nil || p == nil {
		return ""
	}
	return strings.TrimSpace(p.TenantDNS)
}

// Preserve logical priority even when a higher-priority endpoint is a hostname.
func orderedHookDNSEndpoints(endpoints []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range endpoints {
		s, err := validateHookDNSEndpoint(raw)
		if err != nil {
			continue
		}
		if net.ParseIP(s) == nil {
			if ip := upstreamIPCacheGet(s); ip != "" {
				validated, err := validateHookDNSEndpoint(ip)
				if err != nil {
					continue
				}
				s = validated
			}
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func selectHookDNS(c hookDNSConfig, tenant string, rules *domain.ProxyDomainRules, peers []string) hookDNSSelection {
	tenant, _ = validateHookDNSEndpoint(tenant)
	selection := hookDNSSelection{Configured: c.CleanDNS, Tenant: tenant, Source: "default"}
	clean := append([]string{}, c.CleanDNS...)
	if len(c.CleanDNS) > 0 {
		selection.Source = "custom"
	}
	if tenant != "" {
		clean = append(clean, tenant)
		if len(c.CleanDNS) == 0 {
			selection.Source = "tenant"
		}
	}
	policyDNS := ""
	if rules != nil {
		policyDNS, _ = validateHookDNSEndpoint(rules.PlatformDNS)
	}
	if policyDNS != "" {
		clean = append(clean, policyDNS)
		if len(c.CleanDNS) == 0 && tenant == "" {
			selection.Source = "policy"
		}
	}
	if len(clean) == 0 {
		clean = []string{dnsHookPreferredCleanDNS}
	}
	if rules != nil {
		clean = append(clean, rules.TunnelServer, rules.ExitDNS)
	}
	selection.Upstreams = orderedHookDNSEndpoints(append(clean, peers...))
	return selection
}

func hookForeignUpstreams(p *domain.ProxyPolicy, rules *domain.ProxyDomainRules) ([]string, error) {
	c, err := loadHookDNSConfig()
	if err != nil {
		return nil, err
	}
	return selectHookDNS(c, configuredHookTenantDNS(), rules, proxyExitVias(p)).Upstreams, nil
}

func cmdHookDNSConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: mynet proxy dns-hook dns status [--json] | set IP-or-host [...] | clear")
		return 2
	}
	switch args[0] {
	case "set":
		var c hookDNSConfig
		seen := map[string]bool{}
		for _, arg := range args[1:] {
			for _, raw := range strings.FieldsFunc(arg, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
				d, err := validateHookDNSEndpoint(raw)
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					return 2
				}
				if !seen[d] {
					c.CleanDNS = append(c.CleanDNS, d)
					seen[d] = true
				}
			}
		}
		if len(c.CleanDNS) == 0 || len(c.CleanDNS) > 8 {
			fmt.Fprintln(os.Stderr, "请配置1–8个DNS；使用clear恢复跟随租户")
			return 2
		}
		if err := saveHookDNSConfig(c); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "clear":
		if len(args) != 1 {
			return 2
		}
		if err := saveHookDNSConfig(hookDNSConfig{}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "status":
		if len(args) > 2 || len(args) == 2 && args[1] != "--json" {
			return 2
		}
	default:
		return 2
	}
	c, err := loadHookDNSConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	p := EffectiveProxyPolicy()
	var rules *domain.ProxyDomainRules
	if p != nil {
		rules = p.DomainRules
	}
	selection := selectHookDNS(c, configuredHookTenantDNS(), rules, proxyExitVias(p))
	if args[0] == "status" && len(args) == 2 {
		raw, _ := json.Marshal(selection)
		fmt.Println(string(raw))
	} else {
		fmt.Printf("dns: source=%s custom=%s tenant=%s upstreams=%s refresh_seconds=10\n", selection.Source, strings.Join(selection.Configured, ","), selection.Tenant, strings.Join(selection.Upstreams, ","))
	}
	return 0
}
