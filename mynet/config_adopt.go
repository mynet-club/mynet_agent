package mynet

// O1/O8：把既有「旧 Lua 路径」配置**采纳**进 Go 配置档（单一权威）。
//
// 背景：旧部署（mynet_lua / 早期混合形态）把配置写在 Lua 布局 ——
// `conf/zone.json`、`conf/mynet.conf`、`conf/config.json`、`conf/credential.json`，
// 而 Go 侧读的是 `conf/online/<tenant>/profile.json` + `<home>/mode.json`。
// 两者的**配置入口不重合**，于是同一台机器上 Go 说「未配置」、Lua 说「配好了」。
//
// 本采纳做**单向、只读**的收敛：读 Lua 配置 → 写 Go 配置档，让 Go 成为唯一权威。
//   - **只读** Lua 文件，绝不修改/删除它们（回滚有余地）；
//   - Go 配置档已存在则跳过（幂等，绝不覆盖）；
//   - 缺字段/取值未知 → **停止并报告**，不猜（fail-closed）；
//   - 写 marker 记来源与时刻，便于审计。
// 这与 proxy 旧 conf 迁移（proxy_migrate.go）同口径。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

const legacyAdoptMarker = "legacy-adopt.json"

// AdoptResult 采纳结论。
type AdoptResult struct {
	Adopted bool
	Skipped bool
	Reason  string
}

// AdoptLegacyConfig 采纳旧 Lua 配置进 Go 配置档。dry=true 只算不写。
func AdoptLegacyConfig(home string, dry bool) (AdoptResult, error) {
	store := &config.Store{Root: home}

	// Go 配置档已在 → 幂等跳过（不覆盖）
	if m, err := store.LoadMode(); err != nil {
		return AdoptResult{}, fmt.Errorf("读 Go 配置档: %w", err)
	} else if m != nil {
		return AdoptResult{Skipped: true, Reason: "Go 配置档已存在（mode.json）"}, nil
	}

	leg := readLegacy(home)
	if leg == nil {
		return AdoptResult{Skipped: true, Reason: "无旧 Lua 配置（非旧部署）"}, nil
	}

	// fail-closed：必需字段缺一不可
	if leg.Mode != "" && leg.Mode != "online" {
		return AdoptResult{}, fmt.Errorf("旧配置 mode=%q，非 online —— 不猜，留待人工处理", leg.Mode)
	}
	if leg.TenantID == "" {
		return AdoptResult{}, fmt.Errorf("旧配置缺 tenant_id（zone.json/credential.json 都没有）")
	}
	if leg.NodeID == "" {
		return AdoptResult{}, fmt.Errorf("旧配置缺 NODE_ID（mynet.conf）")
	}
	driver := domain.VPNDriverType(leg.Driver)
	if driver == "" {
		driver = domain.DriverTypeGNB
	}
	if driver != domain.DriverTypeGNB && driver != domain.DriverTypeWireGuard {
		return AdoptResult{}, fmt.Errorf("旧配置 VPN_TYPE=%q 未知（非 gnb/wireguard）", leg.Driver)
	}

	prof := &domain.OnlineProfile{
		TenantID:   leg.TenantID,
		TenantName: leg.TenantName,
		APIBase:    leg.APIBase,
		Gateway:    strings.TrimSpace(os.Getenv("MYNET_GATEWAYS")),
		Driver:     driver,
		NodeID:     leg.NodeID,
		NodeName:   leg.NodeName,
	}
	if err := prof.Validate(); err != nil {
		return AdoptResult{}, fmt.Errorf("profile 校验不过: %w", err)
	}

	td, err := domain.TenantDirName(leg.TenantID)
	if err != nil {
		return AdoptResult{}, err
	}
	m, err := domain.NewModeFile(domain.ModeOnline, domain.ActiveTarget{
		TenantID: leg.TenantID,
		NodeID:   leg.NodeID,
		Driver:   driver,
		Source:   "conf/online/" + td,
	})
	if err != nil {
		return AdoptResult{}, fmt.Errorf("mode 校验不过: %w", err)
	}

	if !dry {
		if err := store.SaveOnlineProfile(prof); err != nil {
			return AdoptResult{}, fmt.Errorf("写 profile: %w", err)
		}
		if err := store.SaveMode(&m); err != nil {
			return AdoptResult{}, fmt.Errorf("写 mode.json: %w", err)
		}
		if err := saveLegacyAdoptMarker(home, leg.sources); err != nil {
			return AdoptResult{}, fmt.Errorf("写 marker: %w", err)
		}
	}
	return AdoptResult{Adopted: true,
		Reason: fmt.Sprintf("tenant=%s node=%s driver=%s", leg.TenantID, leg.NodeID, driver)}, nil
}

// configAdopt CLI 入口：mynet config adopt [--dry-run]
func configAdopt(dry bool) int {
	home, code := requireHome()
	if code != 0 {
		return code
	}
	res, err := AdoptLegacyConfig(home, dry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config adopt: %v\n", err)
		return 1
	}
	switch {
	case res.Adopted:
		suffix := ""
		if dry {
			suffix = "（--dry-run：未写盘）"
		}
		fmt.Printf("config adopt: 已采纳旧 Lua 配置 —— %s%s\n", res.Reason, suffix)
	case res.Skipped:
		fmt.Printf("config adopt: 跳过 —— %s\n", res.Reason)
	}
	return 0
}

// legacyConfig 旧 Lua 配置里用得到的字段（其余一律不读，尤其不读 token）。
type legacyConfig struct {
	Mode       string
	TenantID   string
	TenantName string
	APIBase    string
	NodeID     string
	NodeName   string
	Driver     string
	sources    []string
}

// readLegacy 读旧 Lua 配置。一个 file 都没有 → 返回 nil（非旧部署）。
// 文件在但解析不出**必需**字段时由调用方 fail-closed。
func readLegacy(home string) *legacyConfig {
	conf := filepath.Join(home, "conf")
	l := &legacyConfig{}
	found := false

	if raw, err := os.ReadFile(filepath.Join(conf, "config.json")); err == nil {
		found = true
		l.sources = append(l.sources, "conf/config.json")
		var c struct {
			Mode         string `json:"mode"`
			ServerConfig struct {
				APIBaseURL string `json:"api_base_url"`
			} `json:"server_config"`
		}
		if json.Unmarshal(raw, &c) == nil {
			l.Mode = c.Mode
			l.APIBase = c.ServerConfig.APIBaseURL
		}
	}

	if raw, err := os.ReadFile(filepath.Join(conf, "zone.json")); err == nil {
		found = true
		l.sources = append(l.sources, "conf/zone.json")
		var z struct {
			ZoneID   string `json:"zone_id"`
			ZoneName string `json:"zone_name"`
		}
		if json.Unmarshal(raw, &z) == nil {
			l.TenantID = z.ZoneID
			l.TenantName = z.ZoneName
		}
	}

	if raw, err := os.ReadFile(filepath.Join(conf, "mynet.conf")); err == nil {
		found = true
		l.sources = append(l.sources, "conf/mynet.conf")
		kv := parseBashKV(raw)
		l.NodeID = kv["NODE_ID"]
		l.Driver = strings.ToLower(kv["VPN_TYPE"])
	}

	// tenant 回退到 credential.json 的 zone_id（仍不读 token 等其它字段）
	if l.TenantID == "" {
		if raw, err := os.ReadFile(filepath.Join(conf, "credential.json")); err == nil {
			found = true
			l.sources = append(l.sources, "conf/credential.json(zone_id)")
			var c struct {
				ZoneID string `json:"zone_id"`
			}
			if json.Unmarshal(raw, &c) == nil {
				l.TenantID = c.ZoneID
			}
		}
	}

	if !found {
		return nil
	}
	return l
}

// parseBashKV 解析 `KEY="VALUE"` / `KEY=VALUE`（忽略注释与空行）。
func parseBashKV(raw []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		k := strings.ToUpper(strings.TrimSpace(line[:eq]))
		v := strings.Trim(strings.TrimSpace(line[eq+1:]), `"`)
		out[k] = v
	}
	return out
}

func saveLegacyAdoptMarker(home string, sources []string) error {
	mk := struct {
		Schema  int      `json:"schema"`
		Adopted string   `json:"adopted_at"`
		Sources []string `json:"sources"`
		Wrote   []string `json:"wrote"`
	}{
		Schema: 1, Adopted: time.Now().UTC().Format(time.RFC3339),
		Sources: sources, Wrote: []string{"conf/online/<tenant>/profile.json", "conf/mode.json"},
	}
	raw, err := json.MarshalIndent(mk, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "conf", legacyAdoptMarker), append(raw, '\n'), 0o600)
}
