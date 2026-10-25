package mynet

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeBlockConfig(t *testing.T, c dnsAdBlockConfig) {
	t.Helper()
	if err := os.MkdirAll(ProxyStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(dnsAdBlockPath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDNSBlockPrecedesProxyAndDirectWithoutUpstreamOrRoutes(t *testing.T) {
	s, h, calls := newHookHarness(t)
	s.rules.proxied["doubleclick.net"] = true
	s.rules.direct = map[string]bool{"doubleclick.net": true}
	s.rules.block = builtinDNSBlock()
	q := buildDNSQueryMsg("a.doubleclick.net")
	q[0], q[1] = 0xfe, 0x13
	ans := s.answer(q)
	ips := extractDNSAnswerAIDs(ans)
	if len(ips) != 1 || ips[0] != "127.0.0.1" || *calls != 0 || len(h.ops) != 0 || len(s.cache.entries) != 0 || ans[0] != q[0] || ans[1] != q[1] {
		t.Fatalf("answer=%x calls=%d routes=%v", ans, *calls, h.ops)
	}
	if minAnswerTTL(ans) != 60*time.Second {
		t.Fatal("block TTL")
	}
}

func TestDNSBlockRecordTypesAndEDNS(t *testing.T) {
	for _, tc := range []struct {
		typ  uint16
		size int
	}{{1, 4}, {28, 16}, {65, 0}, {5, 0}, {255, 0}} {
		q := buildDNSQueryMsg("doubleclick.net")
		binary.BigEndian.PutUint16(q[len(q)-4:], tc.typ)
		// Query has CD and AD flags and an OPT record; only CD may survive.
		q[3] = 0x30
		binary.BigEndian.PutUint16(q[10:12], 1)
		q = append(q, 0, 0, 41, 4, 208, 0, 0, 0, 0, 0, 0)
		a := dnsBlockedAnswer(q)
		if a[3] != 0x90 || binary.BigEndian.Uint16(a[10:12]) != 0 || a[2]&0x80 == 0 {
			t.Fatalf("bad flags/counts %x", a)
		}
		_, off, e := parseDNSQName(a, 12)
		if e != nil {
			t.Fatal(e)
		}
		if tc.size == 0 {
			if binary.BigEndian.Uint16(a[6:8]) != 0 || len(a) != off+4 {
				t.Fatal("non-address record fabricated")
			}
			continue
		}
		if binary.BigEndian.Uint16(a[len(a)-tc.size-2:]) != uint16(tc.size) {
			t.Fatal("bad record length")
		}
		ip := net.IP(a[len(a)-tc.size:])
		if !ip.IsLoopback() {
			t.Fatalf("not loopback: %v", ip)
		}
	}
}

func TestDNSBlockSuffixBoundaryExactThreatAndAllow(t *testing.T) {
	b := &dnsAdBlock{enabled: true, suffix: map[string]bool{"ads.example": true}, exact: map[string]bool{"bad.customer.example": true}, allow: map[string]bool{"safe.ads.example": true}}
	for _, name := range []string{"ADS.EXAMPLE.", "img.ads.example", "bad.customer.example"} {
		if !b.blocked(name) {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"notads.example", "ads.example.org", "customer.example", "child.bad.customer.example", "img.safe.ads.example"} {
		if b.blocked(name) {
			t.Fatal(name)
		}
	}
	if builtinDNSBlock().blocked("google.com") || builtinDNSBlock().blocked("youtube.com") || len(builtinDNSBlock().exact) < 400 {
		t.Fatal("builtin list invalid")
	}
}

func TestDNSBlockRefreshPurgesCachedAddressAndOwnedRoute(t *testing.T) {
	s, h, calls := newHookHarness(t)
	q := buildDNSQueryMsg("a.test")
	s.answer(q)
	if len(h.s.Routes) == 0 {
		t.Fatal("expected initial route")
	}
	writeBlockConfig(t, dnsAdBlockConfig{Block: []string{"a.test"}})
	if err := s.refresh(sessionRules("a.test"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(h.s.Routes) != 0 || len(s.cache.entries) != 0 {
		t.Fatal("blocked route/cache survived")
	}
	if ips := extractDNSAnswerAIDs(s.answer(q)); len(ips) != 1 || ips[0] != "127.0.0.1" || *calls != 1 {
		t.Fatal("did not block")
	}
	// Invalid replacement must retain the working block rules.
	if err := os.WriteFile(dnsAdBlockPath(), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.refresh(sessionRules("a.test"), time.Now()); err == nil || !s.rules.block.blocked("a.test") {
		t.Fatal("invalid config silently replaced active rules")
	}
	writeBlockConfig(t, dnsAdBlockConfig{Block: []string{"a.test"}, Allow: []string{"a.test"}})
	if err := s.refresh(sessionRules("a.test"), time.Now()); err != nil {
		t.Fatal(err)
	}
	s.answer(q)
	if *calls != 2 || len(h.s.Routes) != 1 {
		t.Fatal("allow override did not restore resolution")
	}
}

func TestDNSBlockCNAMEBeforePinAndCache(t *testing.T) {
	s, h, _ := newHookHarness(t)
	s.rules.block = builtinDNSBlock()
	s.forward = func(_ string, q []byte, _ time.Duration) ([]byte, error) {
		a := append([]byte{}, q...)
		a[2] = 0x81
		a[3] = 0x80
		binary.BigEndian.PutUint16(a[6:8], 2)
		target := buildDNSQueryMsg("ads.doubleclick.net")
		rdata := target[12 : len(target)-4]
		a = append(a, 0xc0, 0x0c, 0, 5, 0, 1, 0, 0, 0, 60, 0, byte(len(rdata)))
		a = append(a, rdata...)
		a = append(a, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 4)
		return a, nil
	}
	a := s.answer(buildDNSQueryMsg("a.test"))
	ips := extractDNSAnswerAIDs(a)
	if len(ips) != 1 || ips[0] != "127.0.0.1" || len(h.ops) != 0 || len(s.cache.entries) != 0 {
		t.Fatal("alias bypassed block")
	}
}

func TestDNSBlockSwitchAndBadConfig(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	if cmdDNSAdBlock([]string{"off"}) != 0 {
		t.Fatal("disable")
	}
	b, _, e := loadDNSAdBlock()
	if e != nil || b.blocked("doubleclick.net") {
		t.Fatal("off not applied")
	}
	if cmdDNSAdBlock([]string{"on"}) != 0 {
		t.Fatal("enable")
	}
	b, _, e = loadDNSAdBlock()
	if e != nil || !b.blocked("doubleclick.net") {
		t.Fatal("on not applied")
	}
	for _, d := range []string{"127.0.0.1", "http://ads.example", "*.com", "foo..com", "-foo.com"} {
		writeBlockConfig(t, dnsAdBlockConfig{Block: []string{d}})
		if _, _, err := loadDNSAdBlock(); err == nil {
			t.Fatalf("accepted %s", d)
		}
	}
	if _, e := os.Stat(filepath.Join(ProxyStateDir(), "dns_adblock.json")); e != nil {
		t.Fatal(e)
	}
}

func TestDNSCompressionLoopRejected(t *testing.T) {
	for _, q := range [][]byte{{0xc0, 0}, {0xc0, 2, 0xc0, 0}, {64, 0}} {
		if _, _, err := parseDNSQName(q, 0); err == nil {
			t.Fatal("invalid compression/label accepted")
		}
	}
}

func BenchmarkDNSBlockLookup(b *testing.B) {
	for _, size := range []int{500, 100000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			r := &dnsAdBlock{enabled: true, suffix: map[string]bool{}, exact: map[string]bool{}, allow: map[string]bool{}}
			for i := 0; i < size; i++ {
				r.suffix[fmt.Sprintf("ad%d.example", i)] = true
			}
			r.suffix["blocked.example"] = true
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !r.blocked("img.blocked.example") {
					b.Fatal("miss")
				}
				if r.blocked("www.clean.example") {
					b.Fatal("false positive")
				}
			}
		})
	}
}
