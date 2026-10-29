package mynet

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/config"
)

func TestCmdProxyDNSHookDomainsStatusEmptyAndAfterSave(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYNET_HOME", dir)
	// ProxyStateDir 依赖 home；先建目录再存基线。
	if err := os.MkdirAll(ProxyStateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if rc := cmdProxyDNSHookDomains([]string{"status"}); rc != 0 {
		t.Fatalf("empty status rc=%d", rc)
	}
	if err := config.SaveProxyDomainBaseline(ProxyStateDir(), &config.ProxyDomainBaseline{
		Proxied: []string{"a.example", "b.example"},
		Version: "vtest",
	}); err != nil {
		t.Fatal(err)
	}
	// status 只读，不碰网络
	if rc := cmdProxyDNSHookDomains([]string{"status"}); rc != 0 {
		t.Fatalf("status after save rc=%d", rc)
	}
	if _, err := os.Stat(filepath.Join(ProxyStateDir(), "proxy_domain_baseline.json")); err != nil {
		t.Fatal(err)
	}
}

func TestCmdProxyDNSHookDomainsRejectsUnknown(t *testing.T) {
	if rc := cmdProxyDNSHookDomains([]string{"nope"}); rc != 2 {
		t.Fatalf("rc=%d", rc)
	}
	if rc := cmdProxyDNSHookDomains(nil); rc != 2 {
		t.Fatalf("empty rc=%d", rc)
	}
}
