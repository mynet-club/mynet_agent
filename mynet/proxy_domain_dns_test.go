package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

func TestResolveDNSSplitFastProxiedUsesPlatform(t *testing.T) {
	r := &domain.ProxyDomainRules{
		Proxied:         []string{"github.com"},
		Direct:          []string{"baidu.com"},
		UpstreamProxied: domain.DNSUpstreamFast,
		UpstreamDirect:  []string{"223.5.5.5"},
		PlatformDNS:     "dns.mynet.club",
	}
	up, via := r.ResolveDNSSplit("github.com")
	if via != domain.DNSUpstreamFast {
		t.Fatalf("via=%s", via)
	}
	if len(up) != 1 || up[0] != "dns.mynet.club" {
		t.Fatalf("fast+proxied 应走平台 DNS，got %v", up)
	}
	// direct → 国内
	up2, via2 := r.ResolveDNSSplit("www.baidu.com")
	if via2 != "direct" || up2[0] != "223.5.5.5" {
		t.Fatalf("direct 应国内 DNS: %v %s", up2, via2)
	}
}

func TestResolveDNSSplitCleanListWins(t *testing.T) {
	r := &domain.ProxyDomainRules{
		Proxied:         []string{"google.com"},
		UpstreamClean:   []string{"google.com"},
		TunnelServer:    "8.8.8.8",
		UpstreamProxied: domain.DNSUpstreamFast,
		PlatformDNS:     "dns.mynet.club",
	}
	up, via := r.ResolveDNSSplit("www.google.com")
	if via != domain.DNSUpstreamClean {
		t.Fatalf("clean 列表应优先，via=%s", via)
	}
	if len(up) != 1 || up[0] != "8.8.8.8" {
		t.Fatalf("clean 应隧道 DNS，got %v", up)
	}
}

func TestResolveDNSSplitSuffixMatch(t *testing.T) {
	r := &domain.ProxyDomainRules{
		Proxied:         []string{".github.com"},
		UpstreamProxied: domain.DNSUpstreamFast,
		PlatformDNS:     "dns.mynet.club",
	}
	if !r.IsProxied("api.github.com") {
		t.Fatal("后缀应命中")
	}
	if !r.IsProxied("github.com") {
		t.Fatal("裸域名应命中")
	}
	if r.IsProxied("github.com.evil.test") {
		t.Fatal("不得误匹配伪装后缀")
	}
}

func TestWriteDomainDNSSplitConfGateway(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mynet-split.conf")
	r := &domain.ProxyDomainRules{
		Proxied:         []string{"github.com"},
		Direct:          []string{"baidu.com"},
		UpstreamClean:   []string{"google.com"},
		TunnelServer:    "8.8.8.8",
		UpstreamProxied: domain.DNSUpstreamFast,
		UpstreamDirect:  []string{"223.5.5.5", "119.29.29.29"},
		PlatformDNS:     "dns.mynet.club",
	}
	if err := WriteDomainDNSSplitConf(r, path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"server=223.5.5.5",
		"server=/google.com/8.8.8.8",
		"server=/github.com/dns.mynet.club",
		"server=/baidu.com/223.5.5.5",
		// T14：proxied/clean 域名解析即进 ipset
		"ipset=/github.com/mynet_proxy",
		"ipset=/google.com/mynet_proxy",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q\n%s", want, text)
		}
	}
	// direct 域名不进 ipset
	if strings.Contains(text, "ipset=/baidu.com") {
		t.Fatalf("direct 不得进 ipset:\n%s", text)
	}
}

func TestPlanDomainDNSForms(t *testing.T) {
	r := &domain.ProxyDomainRules{PlatformDNS: "dns.mynet.club"}
	pc := PlanDomainDNS(r, DNSFormPC, "")
	if pc.Form != DNSFormPC || pc.Nameservers[0] != "dns.mynet.club" {
		t.Fatalf("pc plan=%+v", pc)
	}
	gw := PlanDomainDNS(r, DNSFormGateway, "/tmp/x.conf")
	if gw.Form != DNSFormGateway || gw.ConfPath != "/tmp/x.conf" {
		t.Fatalf("gw plan=%+v", gw)
	}
}

func TestPCResolvPointsToPlatform(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resolv.conf")
	r := &domain.ProxyDomainRules{PlatformDNS: "dns.mynet.club"}
	if _, err := PCNameserverResolv(r, path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "nameserver dns.mynet.club") {
		t.Fatalf("resolv=%s", raw)
	}
}

func TestApplyProxyDNSFilesGatewayWritesConf(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "split.conf")
	p := &domain.ProxyPolicy{
		Enabled: true,
		Role:    "client",
		Region:  domain.ProxyRegionDomestic,
		Policy:  domain.ProxyPolicyWhitelist,
		Split:   &domain.ProxySplit{ListSource: "international", MatchMode: "normal"},
		Exits:   []domain.ProxyExit{{NodeID: "1", Enabled: true}},
		DNS:     &domain.ProxyDNS{Hijack: "lan"},
		DomainRules: &domain.ProxyDomainRules{
			Proxied:     []string{"github.com"},
			PlatformDNS: "dns.mynet.club",
		},
	}
	cmds, err := ApplyProxyDNSFiles(p, DNSFormGateway, conf, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) == 0 {
		t.Fatal("gateway 应有 reload")
	}
	raw, _ := os.ReadFile(conf)
	if !strings.Contains(string(raw), "ipset=/github.com/mynet_proxy") {
		t.Fatalf("conf=%s", raw)
	}
}

func TestLoadSaveDynamicIPSetFile(t *testing.T) {
	dir := t.TempDir()
	path := DynamicIPSetFilePath(dir)
	if err := SaveDynamicIPSetFile(path, []string{"1.2.3.4"}); err != nil {
		t.Fatal(err)
	}
	ips, err := LoadDynamicIPSetFile(path)
	if err != nil || len(ips) != 1 || ips[0] != "1.2.3.4" {
		t.Fatalf("%v %v", ips, err)
	}
	// 不存在 → 空
	ips2, err := LoadDynamicIPSetFile(filepath.Join(dir, "nope.json"))
	if err != nil || ips2 != nil {
		t.Fatalf("%v %v", ips2, err)
	}
}
