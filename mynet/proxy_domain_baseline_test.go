package mynet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

func TestCtlDomainBaselineRefreshReplaceAndFailedDownload(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	t.Setenv("MYNET_CONFIG_HOME", "")
	proxy := "a.test\n*.b.test\na.test\n"
	fail := false
	ctl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/d/plugins/mp/proxy-domains.txt" {
			t.Errorf("unexpected list dependency: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if fail {
			http.Error(w, "offline", 503)
			return
		}
		w.Write([]byte(proxy))
	}))
	defer ctl.Close()
	t.Setenv("MYNET_APPS_INDEX", ctl.URL)
	refresh := func() error {
		return refreshProxyDomainBaseline(context.Background(), ctl.Client(), proxyDomainBaselineURL())
	}
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	first, err := config.LoadProxyDomainBaseline(ProxyStateDir())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Proxied, []string{"a.test", "b.test"}) {
		t.Fatal(first)
	}
	proxy = "new.test\n"
	fail = true
	if err := refresh(); err == nil {
		t.Fatal("failed refresh accepted")
	}
	kept, _ := config.LoadProxyDomainBaseline(ProxyStateDir())
	if !reflect.DeepEqual(first, kept) {
		t.Fatal("failed download replaced valid baseline")
	}
	fail = false
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	next, _ := config.LoadProxyDomainBaseline(ProxyStateDir())
	if !reflect.DeepEqual(next.Proxied, []string{"new.test"}) || next.Version == first.Version {
		t.Fatal("old baseline not replaced")
	}
	proxy = ""
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	empty, _ := config.LoadProxyDomainBaseline(ProxyStateDir())
	if len(empty.Proxied) != 0 || len(empty.Direct) != 0 {
		t.Fatal("empty authoritative baseline did not revoke entries")
	}
}
func TestCtlDomainListRejectsHTML(t *testing.T) {
	if _, err := parseProxyDomainList("<html>error.example</html>"); err == nil {
		t.Fatal("HTML response treated as domain")
	}
}

func TestCtlDomainBaselineURLUsesDistributionPrefix(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"", "https://ctl.mynet.club/d/plugins/mp/"},
		{"https://mirror.test/", "https://mirror.test/d/plugins/mp/"},
	} {
		t.Setenv("MYNET_APPS_INDEX", tc.base)
		if got := proxyDomainBaselineURL(); got != tc.want {
			t.Fatalf("%q: %s", tc.base, got)
		}
	}
}
func TestCtlDirectListAcceptsTLDSuffixes(t *testing.T) {
	got, err := parseProxyDomainList("*.cn\n.中国\n*.qq.com\n")
	if err != nil || !reflect.DeepEqual(got, []string{"cn", "中国", "qq.com"}) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := parseProxyDomainList("not-a-domain"); err == nil {
		t.Fatal("bare word accepted as exact domain")
	}
}

func TestPCWhitelistDoesNotDependOnLegacyDirectOrCleanLists(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	t.Setenv("MYNET_CONFIG_HOME", "")
	if err := SetProxyEnabled(true); err != nil {
		t.Fatal(err)
	}
	st, err := LoadProxyState()
	if err != nil {
		t.Fatal(err)
	}
	st.Local.Policy.DomainRules = &domain.ProxyDomainRules{
		Proxied: []string{"google.com"}, Direct: []string{"google.com"},
		CustomDirect: []string{"private.google.com"}, UpstreamClean: []string{"clean-only.test"},
	}
	if err := SaveProxyState(st); err != nil {
		t.Fatal(err)
	}
	rules := newHookRulesFromPolicy()
	for name, want := range map[string]bool{"google.com": true, "www.google.com": true, "ordinary.test": false, "clean-only.test": false, "private.google.com": false} {
		if got := rules.shouldPin(name); got != want {
			t.Errorf("%s pin=%v want=%v", name, got, want)
		}
	}
}
