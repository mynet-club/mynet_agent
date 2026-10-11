package mynet

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func writeFileT(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 铺一份「旧 Lua 路径」配置。
func seedLegacy(t *testing.T, home string) {
	t.Helper()
	c := filepath.Join(home, "conf")
	writeFileT(t, filepath.Join(c, "config.json"),
		`{"mode":"online","server_config":{"api_base_url":"https://api.mynet.club/api/v2"}}`)
	writeFileT(t, filepath.Join(c, "zone.json"),
		`{"zone_id":"3678250676883169","zone_name":"MyNet Nomadic Village"}`)
	writeFileT(t, filepath.Join(c, "mynet.conf"),
		"VPN_TYPE=\"gnb\"\nNODE_ID=\"8283289760467957\"\n")
}

func TestAdoptLegacyConfig_NoLegacy(t *testing.T) {
	home := t.TempDir()
	res, err := AdoptLegacyConfig(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || res.Adopted {
		t.Fatalf("应跳过: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(home, "mode.json")); !os.IsNotExist(err) {
		t.Fatal("非旧部署不应写 mode.json")
	}
}

func TestAdoptLegacyConfig_AdoptsOnline(t *testing.T) {
	home := t.TempDir()
	seedLegacy(t, home)

	res, err := AdoptLegacyConfig(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Adopted {
		t.Fatalf("应采纳: %+v", res)
	}

	store := &config.Store{Root: home}
	m, err := store.LoadMode()
	if err != nil || m == nil {
		t.Fatalf("mode.json 应写入: %v", err)
	}
	if m.Mode != domain.ModeOnline || m.Active.TenantID != "3678250676883169" ||
		m.Active.NodeID != "8283289760467957" || m.Active.Driver != domain.DriverTypeGNB {
		t.Fatalf("mode 内容错: %+v", m)
	}
	prof, err := store.LoadOnlineProfile("3678250676883169")
	if err != nil {
		t.Fatalf("profile 应写入: %v", err)
	}
	if prof.APIBase != "https://api.mynet.club/api/v2" || prof.TenantName != "MyNet Nomadic Village" {
		t.Fatalf("profile 内容错: %+v", prof)
	}
	// 旧文件不得被动过
	if _, err := os.Stat(filepath.Join(home, "conf", "zone.json")); err != nil {
		t.Fatal("旧 zone.json 不该被删")
	}
	if _, err := os.Stat(filepath.Join(home, "conf", legacyAdoptMarker)); err != nil {
		t.Fatal("应写 marker")
	}

	// 幂等：再跑一次跳过
	res2, err := AdoptLegacyConfig(home, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Skipped {
		t.Fatalf("Go 档已存在应跳过: %+v", res2)
	}
}

func TestAdoptLegacyConfig_FailClosedOnNonOnline(t *testing.T) {
	home := t.TempDir()
	writeFileT(t, filepath.Join(home, "conf", "config.json"), `{"mode":"guest"}`)
	writeFileT(t, filepath.Join(home, "conf", "mynet.conf"), "NODE_ID=\"1\"\n")

	if _, err := AdoptLegacyConfig(home, false); err == nil {
		t.Fatal("mode!=online 应 fail-closed")
	}
	if _, err := os.Stat(filepath.Join(home, "mode.json")); !os.IsNotExist(err) {
		t.Fatal("fail-closed 时不得写 mode.json")
	}
}

func TestAdoptLegacyConfig_FailClosedOnMissingTenant(t *testing.T) {
	home := t.TempDir()
	// 只有 mynet.conf（无 zone.json/credential.json）→ 缺 tenant
	writeFileT(t, filepath.Join(home, "conf", "config.json"), `{"mode":"online"}`)
	writeFileT(t, filepath.Join(home, "conf", "mynet.conf"), "NODE_ID=\"8283289760467957\"\n")

	if _, err := AdoptLegacyConfig(home, false); err == nil {
		t.Fatal("缺 tenant 应 fail-closed")
	}
}

func TestAdoptLegacyConfig_DryRunNoWrite(t *testing.T) {
	home := t.TempDir()
	seedLegacy(t, home)

	res, err := AdoptLegacyConfig(home, true)
	if err != nil || !res.Adopted {
		t.Fatalf("dry-run 应判定可采纳: %+v err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(home, "mode.json")); !os.IsNotExist(err) {
		t.Fatal("dry-run 不得写 mode.json")
	}
}
