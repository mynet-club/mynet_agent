package localapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

const desktopAccelerationUnsupported = "DESKTOP_FIELD_UNSUPPORTED"

func (s *Server) registerAccelerationRoutes() {
	s.mux.HandleFunc("/api/v1/acceleration", s.withConfigMutation(s.handleAcceleration))
	s.mux.HandleFunc("/api/v1/acceleration/apply", s.withConfigMutation(s.handleAccelerationApply))
	s.mux.HandleFunc("/api/v1/acceleration/install", s.withConfigMutation(s.handleAccelerationInstall))
	s.mux.HandleFunc("/api/v1/acceleration/status", getOnly(s.handleAccelerationStatus))
	s.mux.HandleFunc("/api/v1/acceleration/domains", getOnly(s.handleAccelerationDomains))
	s.mux.HandleFunc("/api/v1/acceleration/blocked", s.withConfigMutation(s.handleAccelerationBlocked))
	s.mux.HandleFunc("/api/v1/acceleration/domains/refresh", s.withConfigMutation(s.handleAccelerationDomainsRefresh))
}

// handleAccelerationDomains —— 云端基线 + 本机叠加（事实，只读）。
// 基线清单经执行器读；本机自定义域名来自桌面配置投影。
func (s *Server) handleAccelerationDomains(w http.ResponseWriter, r *http.Request) {
	writeAccelerationDomains(w, s)
}

// handleAccelerationDomainsRefresh —— 手动拉取云端清单。下载在执行器（B2）。
func (s *Server) handleAccelerationDomainsRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "POST only"})
		return
	}
	if s.Ops == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "reason_code": "EXECUTOR_UNAVAILABLE", "message": "本机执行器不可用"})
		return
	}
	if _, err := s.Ops([]string{"proxy", "dns-hook", "domains", "refresh"}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "reason_code": "DOMAIN_REFRESH_FAILED", "retryable": true, "message": err.Error()})
		return
	}
	writeAccelerationDomains(w, s)
}

func writeAccelerationDomains(w http.ResponseWriter, s *Server) {
	cloud := []string{}
	cloudDirect := []string{}
	version := ""
	if s.Ops != nil {
		if data, err := s.opsJSON([]string{"proxy", "dns-hook", "domains", "status"}); err == nil {
			if arr, ok := data["proxied"].([]any); ok {
				cloud = cloud[:0]
				for _, item := range arr {
					if str, _ := item.(string); str != "" {
						cloud = append(cloud, str)
					}
				}
			}
			if arr, ok := data["direct"].([]any); ok {
				cloudDirect = cloudDirect[:0]
				for _, item := range arr {
					if str, _ := item.(string); str != "" {
						cloudDirect = append(cloudDirect, str)
					}
				}
			}
			if v, _ := data["version"].(string); v != "" {
				version = v
			}
		}
	}
	custom := []string{}
	if st, _, err := s.loadProxyState(); err == nil {
		if st.Desktop != nil && st.Desktop.CustomDomains != nil {
			custom = append([]string{}, st.Desktop.CustomDomains...)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data": map[string]any{
			"cloud_domains":   cloud,
			"custom_domains":  custom,
			"cloud_direct":    cloudDirect,
			"custom_blocked":  readCustomBlocked(),
			"blocked_enabled": readBlockedEnabled(),
			"blocked_count":   readBlockedCount(),
			"version":         version,
		},
	})
}

// 拦截名单：dns_adblock.json 的 block/allow（本机自定义）。
func adblockConfigPath() string {
	home := os.Getenv("MYNET_HOME")
	if home == "" {
		home = "."
	}
	return filepath.Join(home, "conf", "local", "dns_adblock.json")
}

type adblockConfigFile struct {
	Enabled *bool    `json:"enabled,omitempty"`
	Block   []string `json:"block,omitempty"`
	Allow   []string `json:"allow,omitempty"`
}

func readAdblockConfig() adblockConfigFile {
	var c adblockConfigFile
	raw, err := os.ReadFile(adblockConfigPath())
	if err != nil {
		return c
	}
	_ = json.Unmarshal(raw, &c)
	return c
}

func readCustomBlocked() []string {
	c := readAdblockConfig()
	if c.Block == nil {
		return []string{}
	}
	return c.Block
}

func readBlockedCount() int {
	// 云端广告/恶意基线规模（dns_block_baseline）；失败时 0，不挡 UI。
	home := os.Getenv("MYNET_HOME")
	if home == "" {
		home = "."
	}
	raw, err := os.ReadFile(filepath.Join(home, "conf", "local", "dns_block_baseline.json"))
	if err != nil {
		return 0
	}
	var b struct {
		Ads     []string `json:"ads"`
		Malware []string `json:"malware"`
	}
	if json.Unmarshal(raw, &b) != nil {
		return 0
	}
	return len(b.Ads) + len(b.Malware)
}

func readBlockedEnabled() bool {
	c := readAdblockConfig()
	return c.Enabled == nil || *c.Enabled
}

// handleAccelerationBlocked —— PUT 本机拦截名单（写 dns_adblock.json.block）。
func (s *Server) handleAccelerationBlocked(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "PUT only"})
		return
	}
	var body struct {
		Domains []string `json:"domains"`
		Enabled *bool    `json:"enabled,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "bad json"})
		return
	}
	c := readAdblockConfig()
	seen := map[string]bool{}
	out := make([]string, 0, len(body.Domains))
	for _, d := range body.Domains {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimSuffix(d, ".")
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	c.Block = out
	if body.Enabled != nil {
		c.Enabled = body.Enabled
	}
	raw, err := json.Marshal(c)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": err.Error()})
		return
	}
	p := adblockConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": err.Error()})
		return
	}
	if err := os.WriteFile(p+".tmp", raw, 0o600); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": err.Error()})
		return
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    map[string]any{"custom_blocked": out, "blocked_enabled": readBlockedEnabled()},
	})
}

// handleAccelerationInstall prepares the local PC runtime. The hook is part of
// the mynet binary, so installation means selecting pc mode and validating that
// the platform-owned runtime can be configured; it does not start DNS takeover.
func (s *Server) handleAccelerationInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "POST only"})
		return
	}
	if s.Ops == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "reason_code": "EXECUTOR_UNAVAILABLE", "message": "本机执行器不可用"})
		return
	}
	if _, err := s.Ops([]string{"config", "form", "pc"}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "reason_code": "PC_RUNTIME_UNAVAILABLE", "retryable": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{"runtime": "pc", "installed": true}})
}

func (s *Server) handleAcceleration(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.writeAcceleration(w, http.StatusOK)
		return
	}
	if r.Method != http.MethodPut {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "GET|PUT only"})
		return
	}
	var cfg domain.DesktopAccelerationConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "bad json"})
		return
	}
	domains, err := domain.NormalizeDesktopDomains(cfg.CustomDomains)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "reason_code": "INVALID_DOMAIN", "message": err.Error()})
		return
	}
	cfg.CustomDomains = domains
	cfg.DNSSources = normalizeDesktopSources(cfg.DNSSources)
	cfg.CustomDNS = normalizeDesktopDNS(cfg.CustomDNS)
	// 加速/DNS 分离：开 DNS 必开加速；加速可不开 DNS
	if err := cfg.ValidateDNSSplit(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "reason_code": "DNS_REQUIRES_ACCEL", "message": err.Error()})
		return
	}
	if cfg.DNSEnabled && len(cfg.DNSSources) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "reason_code": "NO_DNS_SOURCE", "message": "DNS 开启时至少保留一个有值的解析源"})
		return
	}
	st, dir, err := s.loadProxyState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": err.Error()})
		return
	}
	st.Desktop = &cfg
	p := domain.ProxyPolicy{Enabled: cfg.Enabled, LocalOnly: true, Role: domain.ProxyRoleClient, Region: domain.ProxyRegionDomestic, Policy: domain.ProxyPolicyWhitelist, DomainRules: &domain.ProxyDomainRules{CustomDomains: domains}}
	if st.Local != nil {
		p = st.Local.Policy
		p.Enabled, p.LocalOnly, p.Role = cfg.Enabled, true, domain.ProxyRoleClient
		if p.DomainRules == nil {
			p.DomainRules = &domain.ProxyDomainRules{}
		}
		p.DomainRules.CustomDomains = domains
	}
	if err := st.ApplyLocal(p, time.Now()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
		return
	}
	if err := s.saveProxyState(dir, st); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": cfg})
}

func (s *Server) handleAccelerationApply(w http.ResponseWriter, r *http.Request) {
	st, dir, err := s.loadProxyState()
	if err != nil || st.Desktop == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"success": false, "reason_code": "NOT_CONFIGURED", "message": "桌面加速尚未配置"})
		return
	}
	if s.Ops != nil {
		wanted := st.Desktop.Enabled && st.Desktop.DNSEnabled
		if err := s.applyDesktopHook(wanted); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "reason_code": "DNS_HOOK_APPLY_FAILED", "retryable": true, "message": err.Error()})
			return
		}
	}
	_ = dir
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": st.Desktop})
}

func (s *Server) handleAccelerationStatus(w http.ResponseWriter, r *http.Request) {
	st, _, err := s.loadProxyState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": err.Error()})
		return
	}
	data := map[string]any{"config": st.Desktop}
	if s.Ops != nil {
		out, opErr := s.Ops([]string{"proxy", "dns-hook", "status"})
		if opErr != nil {
			data["runtime"] = map[string]any{"state": "unknown", "message": opErr.Error()}
		} else if envelope, ok := out.(map[string]any); ok {
			stdout, _ := envelope["stdout"].(string)
			runtime := parseDesktopHookStatus(stdout)
			// 出口探测优先用 mynetd 内存态（15s 循环）；无注入时才看 stdout 行。
			if s.ExitProbe != nil {
				if probe := s.ExitProbe(); probe != nil {
					runtime["exit_probe"] = probe
				}
			}
			data["runtime"] = runtime
		}
	} else if s.ExitProbe != nil {
		if probe := s.ExitProbe(); probe != nil {
			data["runtime"] = map[string]any{"exit_probe": probe}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": data})
}

var desktopHookStatusPattern = regexp.MustCompile(`dns-hook: pid=(\d+) alive=(true|false) wanted=(true|false) state=([^ ]+) active=(true|false) listen_default=([^ ]+) pins=(\d+) domains=(\d+) routes=(\d+)`)

func parseDesktopHookStatus(stdout string) map[string]any {
	result := map[string]any{"state": "unknown", "raw": ""}
	lines := strings.Split(stdout, "\n")
	if len(lines) > 0 {
		result["raw"] = strings.TrimSpace(lines[0])
	}
	m := desktopHookStatusPattern.FindStringSubmatch(stdout)
	if len(m) != 10 {
		return result
	}
	result["pid"] = parseStatusInt(m[1])
	result["alive"] = m[2] == "true"
	result["wanted"] = m[3] == "true"
	result["state"] = m[4]
	result["active"] = m[5] == "true"
	result["listen"] = m[6]
	result["pins"] = parseStatusInt(m[7])
	result["domains"] = parseStatusInt(m[8])
	result["routes"] = parseStatusInt(m[9])
	if probe := parseExitProbeLine(stdout); probe != nil {
		result["exit_probe"] = probe
	}
	return result
}

// parseExitProbeLine 解析 `exit_probe: key=value …` 一行；无该行返回 nil。
// 字段顺序不固定，按空白切 token 再拆 key=value。
func parseExitProbeLine(stdout string) map[string]any {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "exit_probe:") {
			continue
		}
		probe := map[string]any{}
		for _, tok := range strings.Fields(strings.TrimPrefix(line, "exit_probe:")) {
			k, v, ok := strings.Cut(tok, "=")
			if !ok {
				continue
			}
			switch k {
			case "node":
				if v != "" {
					probe["node_id"] = v
				}
			case "target":
				if v != "" {
					probe["target"] = v
				}
			case "driver":
				if v != "" {
					probe["driver"] = v
				}
			case "method":
				if v != "" {
					probe["method"] = v
				}
			case "link":
				if v != "" {
					probe["link"] = v
				}
			case "reachable":
				probe["reachable"] = v == "true"
			case "rtt_ms":
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					probe["rtt_ms"] = f
				}
			case "age_ms":
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					probe["age_ms"] = n
				}
			case "fails":
				probe["fails"] = parseStatusInt(v)
			}
		}
		if _, ok := probe["reachable"]; ok {
			return probe
		}
	}
	return nil
}

func parseStatusInt(raw string) int {
	n, _ := strconv.Atoi(raw)
	return n
}

func (s *Server) writeAcceleration(w http.ResponseWriter, code int) {
	st, _, err := s.loadProxyState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": err.Error()})
		return
	}
	cfg := st.Desktop
	if cfg == nil {
		cfg = migrateDesktopConfig(st)
	}
	writeJSON(w, code, map[string]any{"success": true, "data": cfg})
}

func migrateDesktopConfig(st *domain.ProxyIntentState) *domain.DesktopAccelerationConfig {
	cfg := &domain.DesktopAccelerationConfig{
		DNSSources: []string{domain.DesktopDNSTenant, domain.DesktopDNSPeer, domain.DesktopDNSCleanPublic},
	}
	if st == nil || st.Local == nil {
		return cfg
	}
	cfg.Enabled = st.Local.Policy.Enabled
	// PC 加速 = dns-hook；迁移时 DNS 开关跟随加速，避免 dns_enabled 永远 false。
	cfg.DNSEnabled = cfg.Enabled
	if rules := st.Local.Policy.DomainRules; rules != nil {
		cfg.CustomDomains, _ = domain.NormalizeDesktopDomains(rules.CustomDomains)
	}
	return cfg
}

func (s *Server) applyDesktopHook(wanted bool) error {
	current := false
	if out, err := s.Ops([]string{"proxy", "dns-hook", "status"}); err == nil {
		if envelope, ok := out.(map[string]any); ok {
			stdout, _ := envelope["stdout"].(string)
			current = strings.Contains(stdout, "state=running") || strings.Contains(stdout, "active=true")
		}
	}
	if current == wanted {
		return nil
	}
	command := []string{"proxy", "dns-hook", "stop"}
	if wanted {
		command = []string{"proxy", "dns-hook", "start"}
	}
	_, err := s.Ops(command)
	return err
}

func normalizeDesktopSources(in []string) []string {
	allowed := map[string]bool{domain.DesktopDNSCustom: true, domain.DesktopDNSTenant: true, domain.DesktopDNSPeer: true, domain.DesktopDNSCleanPublic: true}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		name := strings.TrimSpace(raw)
		if allowed[name] && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func normalizeDesktopDNS(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		value := strings.TrimSpace(raw)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
