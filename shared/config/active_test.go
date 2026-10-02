package config

// mode 切换与 profile/意图读写。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	return &Store{Root: root}
}

func TestUseLocalAndActiveSource(t *testing.T) {
	s := newTestStore(t)
	li := domain.NewLocalIntent(domain.DriverTypeGNB, "n1", "10.0.0.1/32")
	if err := s.SaveLocalIntent(&li); err != nil {
		t.Fatal(err)
	}
	m, err := s.UseLocal()
	if err != nil {
		t.Fatal(err)
	}
	if m.Mode != domain.ModeLocal || m.Active.Driver != domain.DriverTypeGNB {
		t.Fatalf("%+v", m)
	}
	src, got, err := s.ActiveSource()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Mode != domain.ModeLocal {
		t.Fatalf("active=%+v", got)
	}
	if filepath.Base(src) != "local" {
		t.Fatalf("src=%s", src)
	}
	// mode.json 权限
	st, err := os.Stat(filepath.Join(s.Root, "mode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode.json 权限 %v", st.Mode().Perm())
	}
}

func TestUseLocalRequiresIntent(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.UseLocal(); err == nil {
		t.Fatal("无 intent 时 UseLocal 应失败")
	}
}

func TestUseOnlineAndTenants(t *testing.T) {
	s := newTestStore(t)
	p := &domain.OnlineProfile{
		Schema: domain.ConfigSchemaVersion, Mode: domain.ModeOnline,
		TenantID: "t_9", NodeID: "n2", Driver: domain.DriverTypeWireGuard,
	}
	if err := s.SaveOnlineProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOnlineProfile(&domain.OnlineProfile{
		Schema: domain.ConfigSchemaVersion, Mode: domain.ModeOnline,
		TenantID: "t_8", NodeID: "n3", Driver: domain.DriverTypeGNB,
	}); err != nil {
		t.Fatal(err)
	}

	m, err := s.UseOnline("t_9", "n2")
	if err != nil {
		t.Fatal(err)
	}
	if m.Mode != domain.ModeOnline || m.Active.TenantID != "t_9" || m.Active.NodeID != "n2" {
		t.Fatalf("%+v", m)
	}
	if m.Active.Driver != domain.DriverTypeWireGuard {
		t.Fatalf("driver=%s", m.Active.Driver)
	}

	src, got, err := s.ActiveSource()
	if err != nil || got == nil || got.Active.TenantID != "t_9" {
		t.Fatalf("src=%s got=%+v err=%v", src, got, err)
	}
	if filepath.Base(src) != "t_9" {
		t.Fatalf("src=%s", src)
	}

	list, err := s.ListTenants()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("tenants=%v", list)
	}
}

func TestUseOnlineRejectsUnknownTenant(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.UseOnline("nope", "n1"); err == nil {
		t.Fatal("无 profile 应失败")
	}
}

func TestPluginConfigRoundTrip(t *testing.T) {
	s := newTestStore(t)
	base := filepath.Join(s.Root, "conf", "local")
	_ = utils.SafeCreateDir(base, 0o700)
	pc := domain.NewPluginProxy()
	if err := s.SavePlugin(base, &pc); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadPlugin(base, domain.PluginNameProxy)
	if err != nil {
		t.Fatal(err)
	}
	if got.Plugin != domain.PluginNameProxy {
		t.Fatalf("%+v", got)
	}
}

func TestSaveModeRejectsBadTarget(t *testing.T) {
	s := newTestStore(t)
	m := domain.ModeFile{Schema: domain.ConfigSchemaVersion, Mode: domain.ModeOnline,
		Active: domain.ActiveTarget{}} // 缺 tenant
	if err := s.SaveMode(&m); err == nil {
		t.Fatal("online 无 tenant 应拒")
	}
}
