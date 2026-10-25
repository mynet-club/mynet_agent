package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

func TestProxyBaselineUpdatesWithoutShadowingLocalCustoms(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	t.Setenv("MYNET_DATA_HOME", "")
	dir := filepath.Join(root, "conf", "local")
	st := &domain.ProxyIntentState{}
	p := domain.ProxyPolicy{Enabled: true, LocalOnly: true, Role: "client", Region: "domestic", Policy: "whitelist", DomainRules: &domain.ProxyDomainRules{Proxied: []string{"old.test"}, CustomDomains: []string{"mine.test"}, CustomDirect: []string{"direct.test"}}}
	if err := st.ApplyLocal(p, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveProxyIntent(dir, st); err != nil {
		t.Fatal(err)
	}
	if err := SaveProxyDomainBaseline(dir, &ProxyDomainBaseline{Proxied: []string{"new.test"}, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProxyIntent(dir)
	if err != nil {
		t.Fatal(err)
	}
	dr := loaded.Local.Policy.DomainRules
	if !reflect.DeepEqual(dr.Proxied, []string{"new.test"}) || !reflect.DeepEqual(dr.CustomDomains, []string{"mine.test"}) {
		t.Fatal(dr)
	}
	if err := SaveProxyIntent(dir, loaded); err != nil {
		t.Fatal(err)
	}
	if len(loaded.Local.Policy.DomainRules.Proxied) != 1 {
		t.Fatal("save mutated caller")
	}
	raw, _ := os.ReadFile(ProxyIntentPath(dir))
	var disk domain.ProxyIntentState
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Local.Policy.DomainRules.Proxied) != 0 || len(disk.Local.Policy.DomainRules.CustomDomains) != 1 {
		t.Fatal("baseline snapshot persisted into local policy")
	}
	if err := SaveProxyDomainBaseline(dir, &ProxyDomainBaseline{Proxied: []string{"latest.test"}, Version: "v2"}); err != nil {
		t.Fatal(err)
	}
	latest, err := LoadProxyIntent(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(latest.Local.Policy.DomainRules.Proxied, []string{"latest.test"}) {
		t.Fatal("local snapshot shadowed ctl update")
	}
}
