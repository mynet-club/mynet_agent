package mynet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
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
	direct := "cn-only.test\n"
	fail := false
	ctl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/d/plugins/mp/proxy-domains.txt":
			if fail {
				http.Error(w, "offline", 503)
				return
			}
			w.Write([]byte(proxy))
		case "/d/plugins/mp/direct-domains.txt":
			w.Write([]byte(direct))
		default:
			t.Errorf("unexpected list dependency: %s", r.URL.Path)
			http.NotFound(w, r)
		}
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
	if !reflect.DeepEqual(first.Direct, []string{"cn-only.test"}) {
		t.Fatal("direct list not merged", first.Direct)
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
	direct = ""
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

// 三名单优先级（钉死）：拦截 > 直连 > 加速 > 放行。
// 同域冲突按此顺序：入直连即不加速；入拦截则两者都不生效。
func TestPCListPriorityBlockDirectAccelPass(t *testing.T) {
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
		Proxied:       []string{"google.com", "blocked-and-proxied.test"},
		Direct:        []string{"google.com"},
		CustomDirect:  []string{"private.google.com"},
		CustomDomains: []string{"accel-only.test"},
		UpstreamClean: []string{"clean-only.test"},
	}
	if err := SaveProxyState(st); err != nil {
		t.Fatal(err)
	}
	// 云端/本机拦截
	if err := os.WriteFile(dnsAdBlockPath(), []byte(`{"block":["blocked-and-proxied.test"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := newHookRulesFromPolicy()
	for name, want := range map[string]bool{
		// 直连压加速：google.com 同时在 Proxied+Direct → 不钉
		"google.com": false, "www.google.com": false,
		// 拦截压加速：block 命中 → 不钉（且 answer 会拒）
		"blocked-and-proxied.test": false,
		// 仅加速
		"accel-only.test": true,
		// 放行
		"ordinary.test": false, "clean-only.test": false, "private.google.com": false,
	} {
		if got := rules.shouldPin(name); got != want {
			t.Errorf("%s pin=%v want=%v", name, got, want)
		}
	}
}
