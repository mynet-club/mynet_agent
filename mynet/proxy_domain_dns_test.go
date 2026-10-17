package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
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

// IPSetFile 为空（纯域名白名单，无 IP 列表）时，DNS 产物路径必须是
// $MYNET_HOME/var/proxy 下的绝对路径 —— 相对路径 `.dnsmasq.conf` 会落到
// 进程 cwd，mynetd 的 cwd 在只读卷上时 open 失败、整轮 apply 回滚。
func TestProxyDNSConfPathNoIPSetIsAbsolute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	got := ProxyDNSConfPath("")
	want := filepath.Join(home, "var", "proxy", "dns-split.dnsmasq.conf")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("路径必须绝对: %q", got)
	}
}

// 有 IP 列表时产物仍挨着列表文件（好一起清），且带 .dnsmasq.conf 后缀。
func TestProxyDNSConfPathKeepsIPSetSibling(t *testing.T) {
	got := ProxyDNSConfPath("/opt/mynet/var/proxy-lists/interip.txt")
	want := "/opt/mynet/var/proxy-lists/interip.txt.dnsmasq.conf"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// DynamicIPSetFilePath 空 base 也不能返回相对路径（同上，apply 读它）。
func TestDynamicIPSetFilePathEmptyBaseIsAbsolute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	got := DynamicIPSetFilePath("")
	want := filepath.Join(home, "var", "proxy", "dynamic-ipset.json")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("路径必须绝对: %q", got)
	}
}

// 写入失败不得误伤 apply：路径来自 ProxyDNSConfPath 时父目录可创建、文件可落盘。
func TestApplyProxyDNSFilesWritesUnderVarProxy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	p := &domain.ProxyPolicy{
		Enabled: true,
		DomainRules: &domain.ProxyDomainRules{
			Direct:          []string{"baidu.com"},
			Proxied:         []string{"github.com"},
			UpstreamProxied: domain.DNSUpstreamFast,
			PlatformDNS:     "dns.mynet.club",
			UpstreamDirect:  []string{"223.5.5.5"},
		},
	}
	conf := ProxyDNSConfPath("")
	if _, err := ApplyProxyDNSFiles(p, DNSFormGateway, conf, ""); err != nil {
		t.Fatalf("ApplyProxyDNSFiles: %v", err)
	}
	raw, err := os.ReadFile(conf)
	if err != nil {
		t.Fatalf("read %s: %v", conf, err)
	}
	if !strings.Contains(string(raw), "server=/github.com/") {
		t.Fatalf("conf=%s", raw)
	}
}
