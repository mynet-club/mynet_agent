package domain

import (
	"strings"
	"testing"
	"time"
)

func TestMergeDomainRulesUnionAndLaterWins(t *testing.T) {
	early := &ProxyDomainRules{
		Proxied:         []string{"github.com", "old.example"},
		Direct:          []string{"baidu.com"},
		UpstreamProxied: DNSUpstreamClean,
		UpstreamClean:   []string{"google.com"},
		PlatformDNS:     "dns.mynet.club",
		TunnelServer:    "8.8.8.8",
	}
	late := &ProxyDomainRules{
		Proxied:         []string{"github.com", "new.example"},
		UpstreamProxied: DNSUpstreamFast,
		PlatformDNS:     "dns2.mynet.club",
	}
	got := MergeDomainRules(early, late)
	if got.UpstreamProxied != DNSUpstreamFast {
		t.Fatalf("later 标量应胜: %s", got.UpstreamProxied)
	}
	if got.PlatformDNS != "dns2.mynet.club" {
		t.Fatalf("later platform 应胜: %s", got.PlatformDNS)
	}
	if got.TunnelServer != "8.8.8.8" {
		t.Fatal("later 缺省应继承 earlier")
	}
	text := strings.Join(got.Proxied, ",")
	for _, want := range []string{"github.com", "old.example", "new.example"} {
		if !strings.Contains(text, want) {
			t.Fatalf("proxied 应并集: %s", text)
		}
	}
	if strings.Count(text, "github.com") != 1 {
		t.Fatalf("应去重: %s", text)
	}
	if !got.IsClean("www.google.com") {
		t.Fatal("clean 列表应保留")
	}
}

func TestEffectiveDomainRulesFollowsWinner(t *testing.T) {
	now := time.Now()
	s := &ProxyIntentState{}
	cloudPol := ProxyPolicy{
		Enabled: true, Role: "client",
		Region: ProxyRegionDomestic, Policy: ProxyPolicyWhitelist,
		Split:       &ProxySplit{ListSource: ProxyListInternational, MatchMode: ProxyMatchNormal},
		Exits:       []ProxyExit{{NodeID: "9001", Enabled: true}},
		DomainRules: &ProxyDomainRules{Proxied: []string{"from.cloud"}},
	}
	if err := s.ApplyCloud(cloudPol, 5, now); err != nil {
		t.Fatal(err)
	}
	localPol := cloudPol
	localPol.DomainRules = &ProxyDomainRules{
		Proxied:     []string{"from.local"},
		PlatformDNS: "dns.mynet.club",
	}
	if err := s.ApplyLocal(localPol, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	rules := s.EffectiveDomainRules()
	if rules == nil {
		t.Fatal("rules nil")
	}
	// 本地后写生效 → 本地为 later，并集含双方
	if !rules.IsProxied("from.local") || !rules.IsProxied("from.cloud") {
		t.Fatalf("应并集: %+v", rules.Proxied)
	}
}
