package mynet

import (
	"encoding/binary"
	"errors"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"reflect"
	"testing"
	"time"
)

func newHookHarness(t *testing.T) (*dnsHookServer, *sessionHarness, *int) {
	h := newSessionHarness(t)
	calls := new(int)
	s := &dnsHookServer{rules: sessionRules("a.test"), pins: h.s, cache: newDNSRespCache(), tun: func() string { return "utun4" }}
	s.forward = func(up string, q []byte, _ time.Duration) ([]byte, error) {
		*calls++
		return buildTestDNSAnswer(q, "192.0.2.4"), nil
	}
	return s, h, calls
}
func TestHookCachedAnswerRepairsMissingRouteWithoutUpstream(t *testing.T) {
	s, h, calls := newHookHarness(t)
	q := buildDNSQueryMsg("a.test")
	if ans := s.answer(q); ans[3]&15 != 0 {
		t.Fatalf("failed query %x", ans)
	}
	delete(h.fib, "192.0.2.4/32")
	q[0] = 0x55
	ans := s.answer(q)
	if *calls != 1 || ans[0] != 0x55 || h.fib["192.0.2.4/32"] != "utun4" {
		t.Fatalf("calls=%d fib=%v answer=%x", *calls, h.fib, ans)
	}
}
func TestHookPolicyRemovalFlushesCacheAndRoutes(t *testing.T) {
	s, h, calls := newHookHarness(t)
	q := buildDNSQueryMsg("a.test")
	s.answer(q)
	fresh := sessionRules()
	fresh.signature = "removed"
	if err := s.refresh(fresh, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(h.s.Routes) != 0 {
		t.Fatal("removed domain route remains")
	}
	upstream := ""
	s.forward = func(up string, q []byte, _ time.Duration) ([]byte, error) {
		upstream = up
		*calls++
		return buildTestDNSAnswer(q, "203.0.113.8"), nil
	}
	s.answer(q)
	if upstream != "223.5.5.5" || *calls != 2 || len(h.s.Routes) != 0 {
		t.Fatalf("stale cache/routing: %s %d %v", upstream, *calls, h.s.Routes)
	}
}
func TestHookUpstreamSwitchDiscardsOldAddresses(t *testing.T) {
	s, h, _ := newHookHarness(t)
	s.answer(buildDNSQueryMsg("a.test"))
	fresh := sessionRules("a.test")
	fresh.foreign = []string{"10.0.0.2"}
	if err := s.refresh(fresh, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(h.s.Domains) != 0 || len(h.s.Routes) != 0 || len(s.cache.entries) != 0 {
		t.Fatal("old exit state retained")
	}
}
func TestHookInstallFailureIsNotCachedAsSuccess(t *testing.T) {
	s, h, calls := newHookHarness(t)
	h.fail = true
	q := buildDNSQueryMsg("a.test")
	ans := s.answer(q)
	if ans[3]&15 != 2 || len(s.cache.entries) != 0 {
		t.Fatalf("failed route returned success %x", ans)
	}
	h.fail = false
	if ans := s.answer(q); ans[3]&15 != 0 || *calls != 2 {
		t.Fatal("subsequent query did not retry")
	}
}
func TestHookNoTunnelAndUpstreamFailureReturnServfail(t *testing.T) {
	s, h, _ := newHookHarness(t)
	s.tun = func() string { return "" }
	q := buildDNSQueryMsg("a.test")
	if ans := s.answer(q); ans[3]&15 != 2 || len(h.s.Routes) != 0 {
		t.Fatal("no tunnel silently allowed accelerated address")
	}
	s.forward = func(string, []byte, time.Duration) ([]byte, error) { return nil, errors.New("offline") }
	if ans := s.answer(buildDNSQueryMsg("domestic.test")); ans[3]&15 != 2 {
		t.Fatal("upstream timeout must be explicit")
	}
}
func TestHookDirectChildOverridesParentWhiteList(t *testing.T) {
	r := sessionRules("example.com")
	r.direct = map[string]bool{"local.example.com": true}
	if r.shouldPin("a.local.example.com") || !r.shouldPin("api.example.com") {
		t.Fatal("direct exception lost to parent suffix")
	}
	if !reflect.DeepEqual(r.upstreamsFor("local.example.com"), r.domestic) {
		t.Fatal("direct query sent to foreign resolver")
	}
}
func TestHookCacheAgesTTLAndCapsClientTTL(t *testing.T) {
	s, _, _ := newHookHarness(t)
	q := buildDNSQueryMsg("a.test")
	s.forward = func(string, []byte, time.Duration) ([]byte, error) {
		ans := buildTestDNSAnswer(q, "192.0.2.4")
		visitDNSRecords(ans, func(_ uint16, off int) { binary.BigEndian.PutUint32(ans[off:off+4], 86400) })
		return ans, nil
	}
	ans := s.answer(q)
	if ttl := minAnswerTTL(ans); ttl > dnsCacheMaxTTL || ttl < dnsCacheMaxTTL-time.Second {
		t.Fatalf("client TTL=%s", ttl)
	}
	key := dnsCacheKey("a.test", 1)
	e := s.cache.entries[key]
	e.stored = e.stored.Add(-20 * time.Second)
	e.expires = e.expires.Add(-20 * time.Second)
	s.cache.entries[key] = e
	hit := s.answer(q)
	if ttl := minAnswerTTL(hit); ttl > 280*time.Second || ttl < 278*time.Second {
		t.Fatalf("cached TTL=%s", ttl)
	}
}
func TestHookZeroTTLNotCached(t *testing.T) {
	s, _, calls := newHookHarness(t)
	q := buildDNSQueryMsg("a.test")
	s.forward = func(string, []byte, time.Duration) ([]byte, error) {
		*calls++
		ans := buildTestDNSAnswer(q, "192.0.2.4")
		visitDNSRecords(ans, func(_ uint16, off int) { binary.BigEndian.PutUint32(ans[off:off+4], 0) })
		return ans, nil
	}
	s.answer(q)
	s.answer(q)
	if *calls != 2 || len(s.cache.entries) != 0 {
		t.Fatal("TTL=0 cached")
	}
}
func TestDNSAgingDoesNotModifyEDNSFlags(t *testing.T) {
	q := buildDNSQueryMsg("a.test")
	ans := buildTestDNSAnswer(q, "192.0.2.4")
	binary.BigEndian.PutUint16(ans[10:12], 1)
	// OPT, UDP size 1232, DO bit in extended flags.
	ans = append(ans, 0, 0, 41, 4, 208, 0, 0, 128, 0, 0, 0)
	before := append([]byte{}, ans[len(ans)-11:]...)
	ageDNSResponse(ans, 10)
	if !reflect.DeepEqual(before, ans[len(ans)-11:]) {
		t.Fatal("EDNS flags aged as TTL")
	}
}
func TestMergeIPsHandlesIPv4Maximum(t *testing.T) {
	got := MergeIPsToCIDR([]string{"255.255.255.255"})
	if !reflect.DeepEqual(got, []string{"255.255.255.255/32"}) {
		t.Fatal(got)
	}
}

func TestHookStartStopControlIsExclusiveAndReleased(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	called := false
	if rc := withDNSHookControl(func() int {
		if nested := withDNSHookControl(func() int { called = true; return 0 }); nested == 0 {
			t.Fatal("concurrent start/stop accepted")
		}
		return 0
	}); rc != 0 || called {
		t.Fatal("control failed")
	}
	if rc := withDNSHookControl(func() int { return 0 }); rc != 0 {
		t.Fatal("lock not released")
	}
}

func TestHookExitPermissionDoesNotEnableDisabledLocalAcceleration(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	t.Setenv("MYNET_CONFIG_HOME", "")
	st := &domain.ProxyIntentState{
		Local: &domain.StampedProxyPolicy{Policy: domain.ProxyPolicy{Enabled: false, Role: domain.ProxyRoleClient, DomainRules: &domain.ProxyDomainRules{Proxied: []string{"a.test"}, CustomIPs: []string{"192.0.2.4"}}}},
		Cloud: &domain.StampedProxyPolicy{Policy: domain.ProxyPolicy{Enabled: true, Role: domain.ProxyRoleServer}},
	}
	if err := SaveProxyState(st); err != nil {
		t.Fatal(err)
	}
	r := newHookRulesFromPolicy()
	if r.shouldPin("a.test") || len(r.customIPs) != 0 {
		t.Fatal("server permission must not turn local acceleration on")
	}
	st.Local.Policy.Enabled = true
	if err := SaveProxyState(st); err != nil {
		t.Fatal(err)
	}
	if !newHookRulesFromPolicy().shouldPin("a.test") {
		t.Fatal("both-role local acceleration lost")
	}
}
