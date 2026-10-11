package mynet

// O4：旧 proxy conf → 统一 Local 意图。
//
// 现状是两套权威各写各的：
//   - **Lua 旧权威**：`conf/proxy/proxy_role.conf`（OpenWrt LuCI 的 proxy.lua 读写；
//     Go 侧此前**零引用**——grep 为证）；
//   - **Go 统一权威**：`conf/local/proxy_intent.json`（ProxyIntentState，见 proxy_unify.go）。
//
// 后果：同一台机器上「页面开的」与「执行器认的」可能不是一回事。本文件把旧
// conf **单向折叠**进 Local 轨，写幂等 marker，不动 Cloud 轨（出口许可只来自
// 统一 bundle，见 StampCloudIntent）。
//
// 口径（对齐 plan §5.3）：
//   - 只在 Local 轨**为空**时写入，绝不覆盖既有 Local（本地新写入即权威）；
//   - 记迁移标记与版本，重复迁移幂等；
//   - 旧 schema/键名未知则**停止并报告**，不猜（fail-closed）；
//   - 私钥/出口许可不参与：本迁移只搬客户端分流策略，不碰 Cloud 许可。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

const (
	proxyMigrateSchema     = 1
	proxyMigrateMarkerName = "proxy_migrate.json"
)

// proxyMigrateMarker 迁移标记（幂等依据）。放 conf/local 与 intent 同目录。
type proxyMigrateMarker struct {
	SchemaVersion int    `json:"schema_version"`
	Source        string `json:"source"`
	SourceSHA256  string `json:"source_sha256"`
	Result        string `json:"result"`
	MigratedAt    string `json:"migrated_at"`
}

// MigrateResult 迁移结论（给 CLI/日志用，不改变语义）。
type MigrateResult struct {
	Migrated bool
	Skipped  bool
	Reason   string
}

// legacyProxyConfPath 旧 Lua 权威配置（proxy.lua: CONF_DIR/proxy/proxy_role.conf）。
func legacyProxyConfPath(home string) string {
	return filepath.Join(home, "conf", "proxy", "proxy_role.conf")
}

// proxyLocalBaseDir 统一意图的落盘根（与 utils.LocalConfDir 同口径，但不依赖 env）。
func proxyLocalBaseDir(home string) string {
	return filepath.Join(home, "conf", "local")
}

// MigrateLegacyProxyConf 折叠旧 proxy_role.conf 到 Local 轨。dry=true 只算不写。
func MigrateLegacyProxyConf(home string, dry bool) (MigrateResult, error) {
	src := legacyProxyConfPath(home)
	raw, err := os.ReadFile(src)
	if errors.Is(err, os.ErrNotExist) {
		return MigrateResult{Skipped: true, Reason: "无旧 proxy conf（非旧 Lua 部署）"}, nil
	}
	if err != nil {
		return MigrateResult{}, fmt.Errorf("读 %s: %w", src, err)
	}

	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])

	base := proxyLocalBaseDir(home)
	marker, mErr := loadProxyMigrateMarker(base)
	if mErr != nil {
		return MigrateResult{}, mErr
	}
	if marker != nil && marker.SchemaVersion == proxyMigrateSchema && marker.SourceSHA256 == sha &&
		marker.Result == "migrated" {
		// 只有**真迁移过**才算"已迁"。跳过（如 skipped-local-present）也写标记是为了
		// 不反复刷日志，但它不能把后续（残桩判定修好之后）的重试挡死。
		return MigrateResult{Skipped: true, Reason: "已迁移（源未变，sha256 一致）"}, nil
	}

	policy, perr := proxyPolicyFromLegacyConf(raw)
	if perr != nil {
		// fail-closed：不猜键名/值 → 不写盘，把问题交给人和日志。
		return MigrateResult{}, fmt.Errorf("旧 proxy conf 无法安全映射（拒绝猜测）: %w", perr)
	}

	st, err := config.LoadProxyIntent(base)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("读统一意图: %w", err)
	}
	if st.Local != nil && !localIsBareStub(&st.Local.Policy) {
		if !dry {
			_ = saveProxyMigrateMarker(base, &proxyMigrateMarker{
				SchemaVersion: proxyMigrateSchema, Source: "proxy_role.conf",
				SourceSHA256: sha, Result: "skipped-local-present", MigratedAt: nowRFC3339(),
			})
		}
		return MigrateResult{Skipped: true, Reason: "Local 轨已有内容，不覆盖"}, nil
	}

	if err := st.ApplyLocal(*policy, time.Now()); err != nil {
		return MigrateResult{}, fmt.Errorf("写入 Local 轨: %w", err)
	}
	if !dry {
		if err := config.SaveProxyIntent(base, st); err != nil {
			return MigrateResult{}, fmt.Errorf("保存统一意图: %w", err)
		}
		if err := saveProxyMigrateMarker(base, &proxyMigrateMarker{
			SchemaVersion: proxyMigrateSchema, Source: "proxy_role.conf",
			SourceSHA256: sha, Result: "migrated", MigratedAt: nowRFC3339(),
		}); err != nil {
			return MigrateResult{}, fmt.Errorf("写迁移标记: %w", err)
		}
	}
	return MigrateResult{Migrated: true, Reason: "已折叠进 Local 轨"}, nil
}

// localIsBareStub 判定 Local 轨是否只是**空残桩**：除 Enabled 外别无任何策略字段。
// 残桩（例如 `{"enabled":false}` —— 曾被某次 enable/disable 写下的空壳）不是"用户配过的
// 本地策略"，不该挡住旧 Lua conf 的折叠；真有字段（role/region/policy/exits/split/dns/
// domain_rules/acl…）时必须跳过、不覆盖。
//
// 用 JSON 序列化比较：`omitempty` 把空值与 nil 归一，避免 nil-vs-empty 的假不等。
func localIsBareStub(p *domain.ProxyPolicy) bool {
	if p == nil {
		return true
	}
	cp := *p
	cp.Enabled = false // 开关本身不参与"有没有配过"的判定（由折叠决定）
	b, err := json.Marshal(cp)
	if err != nil {
		return false // 序列化失败按"有内容"处理：宁可跳过，不覆盖
	}
	var zero domain.ProxyPolicy
	zb, _ := json.Marshal(zero)
	return string(b) == string(zb)
}

// 旧 conf 的**已知键**白名单（proxy.lua M.save_config 产出的正是这 8 个）。
// 出现白名单外的键 = schema 未知，fail-closed。
var legacyProxyConfKeys = map[string]bool{
	"PROXY_ENABLED":       true,
	"PROXY_MODE":          true,
	"NODE_REGION":         true,
	"PROXY_POLICY":        true,
	"DNS_MODE":            true,
	"DNS_SERVER":          true,
	"DNS_DOMESTIC_SERVER": true,
	"PROXY_PEERS":         true,
}

// proxyPolicyFromLegacyConf 解析 bash KEY="VALUE" 并映射成 ProxyPolicy。
// 不猜：未知键、无法识别的取值一律报错。
func proxyPolicyFromLegacyConf(raw []byte) (*domain.ProxyPolicy, error) {
	kv := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("无法解析的行（非 KEY=VALUE）: %q", line)
		}
		key := strings.ToUpper(strings.TrimSpace(line[:eq]))
		if !legacyProxyConfKeys[key] {
			return nil, fmt.Errorf("未知键 %q（旧 schema 未知，不猜）", key)
		}
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"`)
		kv[key] = val
	}

	enabled := kv["PROXY_ENABLED"] == "1" || strings.EqualFold(kv["PROXY_ENABLED"], "true")

	role := kv["PROXY_MODE"]
	if role == "" {
		role = domain.ProxyRoleClient
	}
	switch role {
	case domain.ProxyRoleClient, domain.ProxyRoleServer, domain.ProxyRoleBoth:
	default:
		return nil, fmt.Errorf("PROXY_MODE 取值未知: %q", role)
	}

	region := kv["NODE_REGION"]
	policy := kv["PROXY_POLICY"]
	if region == "" {
		region = domain.ProxyRegionDomestic
	}
	if policy == "" {
		policy = domain.ProxyPolicyWhitelist
	}
	// 旧版 non_domestic → domestic + blacklist（proxy.lua load_config 同一处理）
	if region == "non_domestic" {
		region = domain.ProxyRegionDomestic
		policy = domain.ProxyPolicyBlacklist
	}

	p := &domain.ProxyPolicy{Enabled: enabled, Role: role, Region: region, Policy: policy}

	if len(kv["PROXY_PEERS"]) > 0 {
		for _, id := range strings.Split(kv["PROXY_PEERS"], ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				p.Exits = append(p.Exits, domain.ProxyExit{NodeID: id, Enabled: true})
			}
		}
	}

	if dnsMode := kv["DNS_MODE"]; dnsMode != "" && dnsMode != "none" {
		d := &domain.ProxyDNS{Mode: dnsMode, Server: kv["DNS_SERVER"]}
		if dom := kv["DNS_DOMESTIC_SERVER"]; dom != "" {
			for _, s := range strings.Split(dom, ",") {
				if s = strings.TrimSpace(s); s != "" {
					d.DomesticServers = append(d.DomesticServers, s)
				}
			}
		}
		p.DNS = d
	}

	// client 侧需要 Split 参数（与 SetProxyEnabled 同口径解算，不手拼）
	if p.IsClientLike() {
		src, mode, err := domain.ResolveProxySplit(p.Region, p.Policy)
		if err != nil {
			return nil, err
		}
		p.Split = &domain.ProxySplit{ListSource: src, MatchMode: mode}
	}

	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

func loadProxyMigrateMarker(base string) (*proxyMigrateMarker, error) {
	raw, err := os.ReadFile(filepath.Join(base, proxyMigrateMarkerName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m proxyMigrateMarker
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("迁移标记损坏: %w", err)
	}
	return &m, nil
}

func saveProxyMigrateMarker(base string, m *proxyMigrateMarker) error {
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(base, proxyMigrateMarkerName), append(raw, '\n'), 0o600)
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
