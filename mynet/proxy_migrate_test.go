package mynet

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

// writeLegacyProxyConf 造一份旧 Lua 权威 conf。
func writeLegacyProxyConf(t *testing.T, home, body string) {
	t.Helper()
	p := legacyProxyConfPath(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadLocalIntent(t *testing.T, home string) *domain.ProxyIntentState {
	t.Helper()
	st, err := config.LoadProxyIntent(proxyLocalBaseDir(home))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// 无旧 conf（非旧 Lua 部署）→ 跳过，不写盘。
func TestMigrateLegacyProxyConf_NoSource(t *testing.T) {
	home := t.TempDir()
	res, err := MigrateLegacyProxyConf(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || res.Migrated {
		t.Fatalf("应跳过: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(proxyLocalBaseDir(home), "proxy_intent.json")); !os.IsNotExist(err) {
		t.Fatal("无源时不应写 intent")
	}
}

// 旧 conf → Local 轨；marker 落盘；重复迁移幂等。
func TestMigrateLegacyProxyConf_MigratesAndIdempotent(t *testing.T) {
	home := t.TempDir()
	writeLegacyProxyConf(t, home, `# MyNet Proxy Configuration
PROXY_ENABLED="1"
PROXY_MODE="client"
NODE_REGION="domestic"
PROXY_POLICY="whitelist"
DNS_MODE="split"
DNS_SERVER="8.8.8.8"
DNS_DOMESTIC_SERVER="223.5.5.5,119.29.29.29"
PROXY_PEERS="1001,1002"
`)

	res, err := MigrateLegacyProxyConf(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Migrated {
		t.Fatalf("应迁移: %+v", res)
	}

	st := loadLocalIntent(t, home)
	if st.Local == nil || !st.Local.Policy.Enabled {
		t.Fatalf("Local 轨应写入且启用: %+v", st.Local)
	}
	p := st.Local.Policy
	if p.Role != domain.ProxyRoleClient || p.Region != domain.ProxyRegionDomestic || p.Policy != domain.ProxyPolicyWhitelist {
		t.Fatalf("策略映射错: %+v", p)
	}
	if p.Split == nil || p.Split.ListSource != domain.ProxyListInternational || p.Split.MatchMode != domain.ProxyMatchNormal {
		t.Fatalf("Split 解算错: %+v", p.Split)
	}
	if len(p.Exits) != 2 || p.Exits[0].NodeID != "1001" || p.Exits[1].NodeID != "1002" {
		t.Fatalf("出口映射错: %+v", p.Exits)
	}
	if p.DNS == nil || p.DNS.Mode != "split" || p.DNS.Server != "8.8.8.8" || len(p.DNS.DomesticServers) != 2 {
		t.Fatalf("DNS 映射错: %+v", p.DNS)
	}
	if st.Cloud != nil {
		t.Fatal("迁移不得触 Cloud 轨")
	}

	// 幂等：源未变 → 跳过
	res2, err := MigrateLegacyProxyConf(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Skipped {
		t.Fatalf("重复迁移应跳过: %+v", res2)
	}
}

// Local 轨已有内容 → 不覆盖（本地新写入即权威）。
func TestMigrateLegacyProxyConf_DoesNotClobberExistingLocal(t *testing.T) {
	home := t.TempDir()
	base := proxyLocalBaseDir(home)
	existing := &domain.ProxyIntentState{}
	if err := existing.ApplyLocal(domain.ProxyPolicy{
		Enabled: true, Role: domain.ProxyRoleClient,
		Region: domain.ProxyRegionIntl, Policy: domain.ProxyPolicyBlacklist,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveProxyIntent(base, existing); err != nil {
		t.Fatal(err)
	}

	writeLegacyProxyConf(t, home, "PROXY_ENABLED=\"1\"\nPROXY_MODE=\"client\"\nNODE_REGION=\"domestic\"\nPROXY_POLICY=\"whitelist\"\n")

	res, err := MigrateLegacyProxyConf(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped {
		t.Fatalf("Local 已有内容应跳过: %+v", res)
	}
	st := loadLocalIntent(t, home)
	if st.Local.Policy.Region != domain.ProxyRegionIntl || st.Local.Policy.Policy != domain.ProxyPolicyBlacklist {
		t.Fatalf("本地既有 Local 被覆盖: %+v", st.Local.Policy)
	}
}

// 空残桩（`{"enabled":false}` —— 某次 enable/disable 留下的空壳）不算"配过"，
// 应被旧 Lua conf 折叠覆盖。这是 2026-10-11 .182 上「代理没起」的根因：残桩把折叠
// 挡死 → Local 停在 enabled:false → Go 单一 owner 下 client 分流就不装了。
func TestMigrateLegacyProxyConf_FoldsBareStubLocal(t *testing.T) {
	home := t.TempDir()
	base := proxyLocalBaseDir(home)
	stub := &domain.ProxyIntentState{}
	if err := stub.ApplyLocal(domain.ProxyPolicy{Enabled: false}, time.Now()); err != nil {
		t.Fatalf("造空残桩失败: %v", err)
	}
	if err := config.SaveProxyIntent(base, stub); err != nil {
		t.Fatal(err)
	}

	writeLegacyProxyConf(t, home, "PROXY_ENABLED=\"1\"\nPROXY_MODE=\"client\"\nNODE_REGION=\"domestic\"\nPROXY_POLICY=\"whitelist\"\n")

	res, err := MigrateLegacyProxyConf(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Migrated {
		t.Fatalf("空残桩应被折叠（不该 skip）: %+v", res)
	}
	st := loadLocalIntent(t, home)
	if st.Local == nil || !st.Local.Policy.Enabled || st.Local.Policy.Role != domain.ProxyRoleClient {
		t.Fatalf("折叠后 Local 应为 client+enabled: %+v", st.Local)
	}
}

// 未知键 → fail-closed：不写 intent、不写 marker。
func TestMigrateLegacyProxyConf_UnknownKeyFailsClosed(t *testing.T) {
	home := t.TempDir()
	writeLegacyProxyConf(t, home, "PROXY_ENABLED=\"1\"\nSOMETHING_NEW=\"x\"\n")

	if _, err := MigrateLegacyProxyConf(home, false); err == nil {
		t.Fatal("未知键应报错（不猜）")
	}
	if _, err := os.Stat(filepath.Join(proxyLocalBaseDir(home), "proxy_intent.json")); !os.IsNotExist(err) {
		t.Fatal("拒绝猜测时不得写 intent")
	}
	if _, err := os.Stat(filepath.Join(proxyLocalBaseDir(home), proxyMigrateMarkerName)); !os.IsNotExist(err) {
		t.Fatal("拒绝猜测时不得写 marker")
	}
}

// 旧值 non_domestic → domestic + blacklist（与 proxy.lua load_config 同口径）。
func TestMigrateLegacyProxyConf_NonDomesticCompat(t *testing.T) {
	home := t.TempDir()
	writeLegacyProxyConf(t, home, "PROXY_ENABLED=\"1\"\nPROXY_MODE=\"client\"\nNODE_REGION=\"non_domestic\"\n")

	res, err := MigrateLegacyProxyConf(home, false)
	if err != nil || !res.Migrated {
		t.Fatalf("应迁移: %+v err=%v", res, err)
	}
	p := loadLocalIntent(t, home).Local.Policy
	if p.Region != domain.ProxyRegionDomestic || p.Policy != domain.ProxyPolicyBlacklist {
		t.Fatalf("non_domestic 兼容映射错: %+v", p)
	}
}

// dry-run 只算不写。
func TestMigrateLegacyProxyConf_DryRunDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	writeLegacyProxyConf(t, home, "PROXY_ENABLED=\"1\"\nPROXY_MODE=\"client\"\nNODE_REGION=\"domestic\"\nPROXY_POLICY=\"whitelist\"\n")

	res, err := MigrateLegacyProxyConf(home, true)
	if err != nil || !res.Migrated {
		t.Fatalf("dry-run 应判定为可迁移: %+v err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(proxyLocalBaseDir(home), "proxy_intent.json")); !os.IsNotExist(err) {
		t.Fatal("dry-run 不得写 intent")
	}
}
