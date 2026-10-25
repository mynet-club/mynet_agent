package mynet

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func TestHookDNSLocalTenantPeerPriorityAndHostnameOrder(t *testing.T) {
	upstreamIPCache.Delete("tenant-dns.example")
	r := &domain.ProxyDomainRules{PlatformDNS: "tenant-dns.example", TunnelServer: "10.0.0.1", ExitDNS: "10.0.0.2"}
	c := hookDNSConfig{CleanDNS: []string{"221.216.138.69", "custom-dns.example"}}
	s := selectHookDNS(c, "tenant-dns.example", r, []string{"10.0.0.1", "10.0.0.3"})
	want := []string{"221.216.138.69", "custom-dns.example", "tenant-dns.example", "10.0.0.1", "10.0.0.2", "10.0.0.3"}
	if s.Source != "custom" || !reflect.DeepEqual(s.Upstreams, want) {
		t.Fatal(s)
	}
	s = selectHookDNS(hookDNSConfig{}, "tenant-dns.example", r, []string{"10.0.0.3"})
	if s.Source != "tenant" || s.Upstreams[0] != "tenant-dns.example" {
		t.Fatal("peer IP outranked tenant hostname", s)
	}
	upstreamIPCachePut("tenant-dns.example", "203.0.113.9")
	defer upstreamIPCache.Delete("tenant-dns.example")
	s = selectHookDNS(hookDNSConfig{}, "tenant-dns.example", r, nil)
	if s.Upstreams[0] != "203.0.113.9" {
		t.Fatal("bootstrap changed priority", s)
	}
	if s = selectHookDNS(hookDNSConfig{}, "", nil, nil); s.Source != "default" || !reflect.DeepEqual(s.Upstreams, []string{dnsHookPreferredCleanDNS}) {
		t.Fatal(s)
	}
}

func TestHookDNSConfigPersistsLocallyAndClearFollowsNewTenant(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	store := &config.Store{}
	profile := &domain.OnlineProfile{Schema: domain.ConfigSchemaVersion, Mode: domain.ModeOnline, TenantID: "t1", Driver: domain.DriverTypeGNB, NodeID: "1001", TenantDNS: "198.51.100.1"}
	if err := store.SaveOnlineProfile(profile); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UseOnline("t1", "1001"); err != nil {
		t.Fatal(err)
	}
	if cmdHookDNSConfig([]string{"set", "221.216.138.69, 221.216.138.69", "custom-dns.example"}) != 0 {
		t.Fatal("save failed")
	}
	c, err := loadHookDNSConfig()
	if err != nil || len(c.CleanDNS) != 2 {
		t.Fatal(c, err)
	}
	before, _ := os.ReadFile(hookDNSConfigPath())
	profile.TenantDNS = "198.51.100.2"
	if err := store.SaveOnlineProfile(profile); err != nil {
		t.Fatal(err)
	}
	if tenant := configuredHookTenantDNS(); tenant != "198.51.100.2" {
		t.Fatal(tenant)
	}
	if got := selectHookDNS(c, configuredHookTenantDNS(), nil, nil); got.Upstreams[0] != "221.216.138.69" || got.Upstreams[2] != "198.51.100.2" {
		t.Fatal(got)
	}
	after, _ := os.ReadFile(hookDNSConfigPath())
	if string(before) != string(after) {
		t.Fatal("tenant update overwrote device config")
	}
	if cmdHookDNSConfig([]string{"clear"}) != 0 {
		t.Fatal("clear failed")
	}
	c, err = loadHookDNSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := selectHookDNS(c, configuredHookTenantDNS(), nil, nil); got.Source != "tenant" || got.Upstreams[0] != "198.51.100.2" {
		t.Fatal(got)
	}
	localMode, err := domain.NewModeFile(domain.ModeLocal, domain.ActiveTarget{Driver: domain.DriverTypeGNB, Source: "conf/local"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMode(&localMode); err != nil {
		t.Fatal(err)
	}
	if configuredHookTenantDNS() != "" {
		t.Fatal("inactive tenant DNS leaked into local mode")
	}
}

func TestHookDNSRejectsInvalidSettingsWithoutReplacingConfig(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	if cmdHookDNSConfig([]string{"set", "221.216.138.69"}) != 0 {
		t.Fatal("save")
	}
	old, _ := os.ReadFile(hookDNSConfigPath())
	for _, value := range []string{"127.0.0.1", "::1", "0.0.0.0", "::", "224.0.0.1", "http://dns.example", "dns.example:53", "*.example.com"} {
		if cmdHookDNSConfig([]string{"set", value}) == 0 {
			t.Fatal("invalid endpoint accepted", value)
		}
		actual, _ := os.ReadFile(hookDNSConfigPath())
		if string(actual) != string(old) {
			t.Fatal("invalid input replaced good config")
		}
	}
}

func TestHookDNSRefreshChangesUpstreamAndBadConfigKeepsWorkingRules(t *testing.T) {
	s, h, _ := newHookHarness(t)
	q := buildDNSQueryMsg("a.test")
	s.answer(q)
	if len(h.s.Routes) == 0 {
		t.Fatal("missing initial route")
	}
	if err := saveHookDNSConfig(hookDNSConfig{CleanDNS: []string{"221.216.138.69"}}); err != nil {
		t.Fatal(err)
	}
	fresh := sessionRules("a.test")
	fresh.foreign, fresh.upstreamError = hookForeignUpstreams(nil, nil)
	if err := s.refresh(fresh, time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.rules.foreign[0] != "221.216.138.69" || len(h.s.Routes) != 0 || len(s.cache.entries) != 0 {
		t.Fatal("changed upstream did not clear old addresses")
	}
	if err := os.WriteFile(hookDNSConfigPath(), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	fresh.foreign, fresh.upstreamError = hookForeignUpstreams(nil, nil)
	if err := s.refresh(fresh, time.Now()); err == nil || s.rules.foreign[0] != "221.216.138.69" {
		t.Fatal("bad config replaced working resolver")
	}
}
