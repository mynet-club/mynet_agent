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

// selectHookDNS 组装加速域名解析链。
//
// 模型（2026-10-28 用户钉死）：
//   - 四源优先级 DNSSources = [custom, tenant, peer, clean_public]，**顺序即生效链**；
//   - **有值的源才参与**（custom=CustomDNS，tenant=租户 DNS，peer=出口 VIP，
//     clean_public=内置公共兜底）；
//   - 加速与 DNS 分离：开 DNS 必开加速，加速可不开 DNS。
//
// 污染防线：若将 ISP DNS 配进 custom 且排最前，t66y 类域名会被投毒——
// 这是用户显式配置的结果；默认顺序把 peer/clean_public 放前。
func selectHookDNS(sources []string, customDNS []string, tenant string, rules *domain.ProxyDomainRules, peers []string) hookDNSSelection {
	tenant, _ = validateHookDNSEndpoint(tenant)
	selection := hookDNSSelection{Configured: customDNS, Tenant: tenant, Source: "default"}

	// 各源的**值**（无值则该源不参与）
	values := map[string][]string{
		domain.DesktopDNSCustom:      normalizeDNSList(customDNS),
		domain.DesktopDNSTenant:      nil,
		domain.DesktopDNSPeer:        normalizeDNSList(peers),
		domain.DesktopDNSCleanPublic: loadCleanDNSList(),
	}
	if tenant != "" {
		values[domain.DesktopDNSTenant] = []string{tenant}
	}
	if rules != nil {
		if ps, err := validateHookDNSEndpoint(rules.PlatformDNS); err == nil && ps != "" {
			// 平台 DNS 归 tenant 源后备（有租户 DNS 时排后）
			values[domain.DesktopDNSTenant] = append(values[domain.DesktopDNSTenant], ps)
		}
		if rules.TunnelServer != "" || rules.ExitDNS != "" {
			values[domain.DesktopDNSPeer] = append(values[domain.DesktopDNSPeer], rules.TunnelServer, rules.ExitDNS)
		}
	}

	// 按优先级取有值的源
	if len(sources) == 0 {
		sources = []string{domain.DesktopDNSCustom, domain.DesktopDNSTenant, domain.DesktopDNSPeer, domain.DesktopDNSCleanPublic}
	}
	var chain []string
	for _, name := range sources {
		vals := normalizeDNSList(values[name])
		if len(vals) == 0 {
			continue // 无值不生效
		}
		chain = append(chain, vals...)
		if selection.Source == "default" {
			selection.Source = name // 首个有值源
		}
	}
	if len(chain) == 0 {
		// 全无值：兜底公共干净 DNS 清单（一份两用清单），避免零上游
		chain = append([]string{}, builtinCleanDNSList...)
		selection.Source = "default"
	}
	selection.Upstreams = orderedHookDNSEndpoints(chain)
	return selection
}

func normalizeDNSList(list []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, raw := range list {
		d, err := validateHookDNSEndpoint(raw)
		if err != nil || d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// desktopDNSInputs 读桌面加速配置的四源优先级与 custom 值。
// custom 值：CustomDNS 优先；无则回落 dns_hook.json.clean_dns（兼容旧配置）。
func desktopDNSInputs() (sources, customDNS []string, err error) {
	if st, lerr := LoadProxyState(); lerr == nil && st != nil && st.Desktop != nil {
		sources = st.Desktop.DNSSources
		customDNS = st.Desktop.CustomDNS
	}
	if len(customDNS) == 0 {
		// 回落 clean_dns（兼容旧配置）。配置损坏必须报错：否则会用空链
		// 替换工作中的解析器（TestHookDNSRefresh…）。
		c, cerr := loadHookDNSConfig()
		if cerr != nil {
			return nil, nil, cerr
		}
		customDNS = c.CleanDNS
	}
	return sources, customDNS, nil
}

func hookForeignUpstreams(p *domain.ProxyPolicy, rules *domain.ProxyDomainRules) ([]string, error) {
	sources, customDNS, err := desktopDNSInputs()
	if err != nil {
		return nil, err
	}
	return selectHookDNS(sources, customDNS, configuredHookTenantDNS(), rules, proxyExitVias(p)).Upstreams, nil
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
	sources, customDNS, err := desktopDNSInputs()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	p := EffectiveProxyPolicy()
	var rules *domain.ProxyDomainRules
	if p != nil {
		rules = p.DomainRules
	}
	selection := selectHookDNS(sources, customDNS, configuredHookTenantDNS(), rules, proxyExitVias(p))
	if args[0] == "status" && len(args) == 2 {
		raw, _ := json.Marshal(selection)
		fmt.Println(string(raw))
	} else {
		fmt.Printf("dns: source=%s custom=%s tenant=%s upstreams=%s refresh_seconds=10\n", selection.Source, strings.Join(selection.Configured, ","), selection.Tenant, strings.Join(selection.Upstreams, ","))
	}
	return 0
}
