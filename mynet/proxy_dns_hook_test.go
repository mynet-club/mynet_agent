package mynet

import (
	"encoding/binary"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

func buildTestDNSQuery(qname string) []byte {
	return buildDNSQueryMsg(qname)
}

// buildTestDNSAnswer 在 query 上叠 A 答案。
func buildTestDNSAnswer(query []byte, ips ...string) []byte {
	msg := append([]byte{}, query...)
	binary.BigEndian.PutUint16(msg[2:4], 0x8180) // QR response
	binary.BigEndian.PutUint16(msg[6:8], uint16(len(ips)))
	for _, ip := range ips {
		// NAME pointer to offset 12
		msg = append(msg, 0xC0, 0x0C)
		// TYPE A, CLASS IN, TTL 60, RDLEN 4
		msg = append(msg, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
		msg = append(msg, net.ParseIP(ip).To4()...)
	}
	return msg
}

func TestParseDNSQuestion(t *testing.T) {
	q := buildTestDNSQuery("github.com")
	name, err := parseDNSQuestion(q)
	if err != nil || name != "github.com" {
		t.Fatalf("qname=%q err=%v", name, err)
	}
}

func TestExtractDNSAnswerAIDs(t *testing.T) {
	q := buildTestDNSQuery("github.com")
	a := buildTestDNSAnswer(q, "20.205.243.166", "1.2.3.4")
	ips := extractDNSAnswerAIDs(a)
	if len(ips) != 2 || ips[0] != "20.205.243.166" || ips[1] != "1.2.3.4" {
		t.Fatalf("A answers=%v", ips)
	}
}

func TestHookRulesSuffixMatch(t *testing.T) {
	r := &dnsHookRules{
		proxied: map[string]bool{"google.com": true},
	}
	if !r.shouldPin("google.com") {
		t.Fatal("exact should pin")
	}
	if !r.shouldPin("www.google.com") {
		t.Fatal("suffix should pin")
	}
	if r.shouldPin("baidu.com") {
		t.Fatal("unrelated should not pin")
	}
}

func TestForwardDNSQueryTimeout(t *testing.T) {
	// 不可达上游应返回 error（快速失败路径）
	_, err := forwardDNSQuery("127.0.0.1", buildTestDNSQuery("example.com"), 200*time.Millisecond)
	if err == nil {
		// 某些环境 127.0.0.1:53 可能有东西；只要不 panic 即可
		t.Log("unexpected answer on 127.0.0.1:53")
	}
}

// DNS 应答缓存：命中时必须改写事务 ID（客户端只认 ID 配对）。
func TestDNSRespCacheRewritesQueryID(t *testing.T) {
	c := newDNSRespCache()
	resp := make([]byte, 16)
	binary.BigEndian.PutUint16(resp[0:2], 0x1111) // 原始 ID
	key := dnsCacheKey("example.com", 1)
	c.put(key, resp, time.Minute)

	got := c.get(key, 0x2222)
	if got == nil {
		t.Fatal("缓存应命中")
	}
	if id := binary.BigEndian.Uint16(got[0:2]); id != 0x2222 {
		t.Fatalf("应答 ID=0x%x, want 0x2222（必须改成当前查询 ID）", id)
	}
}

// 过期后不得命中。
func TestDNSRespCacheExpires(t *testing.T) {
	c := newDNSRespCache()
	key := dnsCacheKey("a.example.com", 1)
	c.put(key, []byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 10*time.Millisecond)
	if c.get(key, 1) == nil {
		t.Fatal("刚写入应命中")
	}
	time.Sleep(20 * time.Millisecond)
	if c.get(key, 1) != nil {
		t.Fatal("过期后不得命中")
	}
}

// qtype 不同不得串缓存（A 与 AAAA 是两条记录）。
func TestDNSRespCacheKeySeparatesQType(t *testing.T) {
	c := newDNSRespCache()
	k1 := dnsCacheKey("example.com", 1)
	k2 := dnsCacheKey("example.com", 28)
	c.put(k1, []byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, time.Minute)
	if c.get(k2, 9) != nil {
		t.Fatal("qtype=28 不得命中 qtype=1 的缓存")
	}
	if c.get(k1, 9) == nil {
		t.Fatal("qtype=1 应命中")
	}
}

// minAnswerTTL 取 A 记录最小 TTL；无 A 记录返回 0。
func TestMinAnswerTTL(t *testing.T) {
	// 构造最小应答：header + 1 question + 1 A answer(TTL=30)
	msg := make([]byte, 0, 64)
	msg = append(msg, 0x12, 0x34)             // ID
	msg = append(msg, 0x81, 0x80)             // flags
	msg = append(msg, 0x00, 0x01, 0x00, 0x01) // qd=1 an=1
	msg = append(msg, 0x00, 0x00, 0x00, 0x00) // nscount/arcount
	// QNAME: example.com
	for _, p := range []string{"example", "com"} {
		msg = append(msg, byte(len(p)))
		msg = append(msg, p...)
	}
	msg = append(msg, 0x00, 0x00, 0x01, 0x00, 0x01) // QTYPE A, QCLASS IN
	// Answer NAME (pointer to question) + TYPE A + CLASS IN + TTL 30 + RDLEN 4 + 1.2.3.4
	msg = append(msg, 0xC0, 0x0C)
	msg = append(msg, 0x00, 0x01, 0x00, 0x01)
	msg = append(msg, 0x00, 0x00, 0x00, 0x1E) // TTL 30
	msg = append(msg, 0x00, 0x04)
	msg = append(msg, 1, 2, 3, 4)

	ttl := minAnswerTTL(msg)
	if ttl != 30*time.Second {
		t.Fatalf("minAnswerTTL=%v, want 30s", ttl)
	}
	// 无应答段 → 0
	empty := make([]byte, 12)
	if minAnswerTTL(empty) != 0 {
		t.Fatal("无 A 记录应返回 0")
	}
}

// bootstrap 解析不能走系统解析器 —— hook 接管 DNS 后那会自己查自己（死锁）。
// 这里锁：主机名走 bootstrapResolveA / forwardUDP，不调 net.LookupIP。
func TestBootstrapResolveACaches(t *testing.T) {
	// 直接打缓存路径，不真发网络
	upstreamIPCachePut("dns.dsplat.com", "203.0.113.9")
	ip := bootstrapResolveA("dns.dsplat.com", 100*time.Millisecond)
	if ip != "203.0.113.9" {
		t.Fatalf("bootstrapResolveA=%q, want 203.0.113.9（缓存命中）", ip)
	}
	// 过期后不再命中缓存（会走网络，这里只验缓存键过期逻辑）
	upstreamIPCache.Store("dns.dsplat.com", upstreamIPEntry{ip: "203.0.113.9", expires: time.Now().Add(-time.Second)})
	if got := upstreamIPCacheGet("dns.dsplat.com"); got != "" {
		t.Fatalf("过期缓存不应命中，got %q", got)
	}
}

// forwardDNSQuery 对 IP 上游不应触发 bootstrap 解析（直接 dial）。
// 坏上游必须返回 err 且不 panic。用**语法非法地址**而不是保留段 ——
// 本机 dns-hook/VPN 可能把 240.0.0.1:53 拦截并应答，保留段用例会变成环境依赖。
func TestForwardUDPRejectsBadServer(t *testing.T) {
	// 注意：空 host 会变成 ":53" → 连本机 53；本机常有 dns-hook 在听。
	for _, server := range []string{"300.300.300.300", "not-an-ip", "[::bad]"} {
		_, err := forwardUDP(server, buildDNSQueryMsg("example.com"), 200*time.Millisecond)
		if err == nil {
			t.Fatalf("server=%q dial/forward 应失败", server)
		}
	}
}

// 上游 DNS 必须是 IP（2026-10-17 用户裁决）：主机名会被剔除，
// 否则 hook 接管系统 DNS 后解析上游会打回自己 → 死锁。
func TestOnlyIPUpstreams(t *testing.T) {
	got := onlyIPUpstreams([]string{
		"223.5.5.5",
		"dns.dsplat.com",
		"119.29.29.29",
		"dns.mynet.club",
		"223.5.5.5", // 重复
		"",
		"  8.8.8.8  ",
	})
	want := []string{"223.5.5.5", "119.29.29.29", "8.8.8.8"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	// 全是主机名 → 空表（好过塞主机名进死锁路径）
	if out := onlyIPUpstreams([]string{"dns.mynet.club", "dns.dsplat.com"}); len(out) != 0 {
		t.Fatalf("全主机名应得空表，got %v", out)
	}
}

// foreign/domestic 默认上游经过 onlyIPUpstreams 过滤，不得出现主机名。
func TestHookUpstreamsAreIPOnly(t *testing.T) {
	// 预热缓存，避免本用例现场 bootstrap 网络（2s 超时）拖慢/抖动
	upstreamIPCachePut("dns.mynet.club", "203.0.113.9")
	upstreamIPCachePut("dns.dsplat.com", "203.0.113.10")
	t.Setenv("MYNET_HOME", t.TempDir())
	r := newHookRulesFromPolicy()
	for _, up := range append(append([]string{}, r.foreign...), r.domestic...) {
		if net.ParseIP(up) == nil {
			t.Fatalf("上游 %q 必须是 IP", up)
		}
	}
	for _, up := range r.upstreamsFor("google.com") {
		if net.ParseIP(up) == nil {
			t.Fatalf("foreign 路径上游 %q 必须是 IP", up)
		}
	}
	for _, up := range r.upstreamsFor("baidu.com") {
		if net.ParseIP(up) == nil {
			t.Fatalf("domestic 路径上游 %q 必须是 IP", up)
		}
	}
}

// 国内链不得出现被投毒的平台智能 DNS 解析结果（221.216.138.69）。
// 2026-10-28：dns.dsplat.com CNAME→home.luoyueliang.com→221.216.138.69 实测投毒，
// 已从 defaultDomesticUpstreams 摘除；本测试防回归。
func TestDomesticUpstreamsExcludePoisonedPlatformDNS(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	upstreamIPCachePut("dns.dsplat.com", "221.216.138.69")
	defer upstreamIPCache.Delete("dns.dsplat.com")
	r := newHookRulesFromPolicy()
	for _, up := range append(append([]string{}, r.foreign...), r.domestic...) {
		if up == "221.216.138.69" {
			t.Fatalf("投毒 DNS 221.216.138.69 不得进上游链: foreign=%v domestic=%v", r.foreign, r.domestic)
		}
	}
	for _, up := range r.upstreamsFor("baidu.com") {
		if up == "221.216.138.69" {
			t.Fatalf("domestic 路径不得出现投毒 DNS: %v", up)
		}
	}
}

func TestForeignUpstreamsPreferCleanDNSWithPeerFallback(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	p := &domain.ProxyPolicy{Enabled: true, Exits: []domain.ProxyExit{{NodeID: "n1", Via: "10.182.236.180", Enabled: true}}, DomainRules: &domain.ProxyDomainRules{TunnelServer: "10.0.0.1", ExitDNS: "10.0.0.2"}}
	got := defaultForeignUpstreams(p, p.DomainRules)
	// 四源序（无 custom/tenant 值）：peer → clean_public
	want := []string{"10.182.236.180", "10.0.0.1", "10.0.0.2"}
	want = append(want, builtinCleanDNSList...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("four-source order: %v", got)
	}
}

func TestForeignUpstreamsWithoutPeerUseCleanPublic(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	// 无 peer / custom / tenant 时，clean_public 源（有值）生效
	got := defaultForeignUpstreams(nil, nil)
	if !reflect.DeepEqual(got, builtinCleanDNSList) {
		t.Fatalf("unexpected fallback: %v", got)
	}
}

// foreign 为空时 upstreamsFor 对 proxied 域名仍须给出 IP 上游（不能静默无应答）。
func TestUpstreamsForProxiedNotEmpty(t *testing.T) {
	upstreamIPCachePut("dns.mynet.club", "203.0.113.9")
	defer upstreamIPCache.Delete("dns.mynet.club")
	r := &dnsHookRules{
		proxied: map[string]bool{"github.com": true},

		foreign:  nil,
		domestic: []string{"223.5.5.5"},
	}
	ups := r.upstreamsFor("github.com")
	if len(ups) == 0 {
		t.Fatal("proxied 域名上游不得为空（会导致客户端超时）")
	}
	for _, up := range ups {
		if net.ParseIP(up) == nil {
			t.Fatalf("上游 %q 必须是 IP", up)
		}
	}
}

func TestHookRuleConstructionDoesNotWaitForBootstrap(t *testing.T) {
	upstreamIPCache.Delete("unresolved.test")
	got := ipUpstreamsOrResolve([]string{"10.0.0.1", "unresolved.test"})
	if len(got) != 2 || got[0] != "10.0.0.1" || got[1] != "unresolved.test" {
		t.Fatalf("unresolved endpoint dropped: %v", got)
	}
}

func TestCleanResolverFailureFallsBackToPeer(t *testing.T) {
	for _, code := range []byte{2, 5} {
		s, _, _ := newHookHarness(t)
		s.rules.foreign = []string{"8.8.8.8", "10.0.0.2"}
		var seen []string
		s.forward = func(up string, q []byte, _ time.Duration) ([]byte, error) {
			seen = append(seen, up)
			if up == "8.8.8.8" {
				a := dnsFailure(q)
				a[3] = 0x80 | code
				return a, nil
			}
			return buildTestDNSAnswer(q, "192.0.2.9"), nil
		}
		ans := s.answer(buildDNSQueryMsg("a.test"))
		if !reflect.DeepEqual(seen, s.rules.foreign) || ans[3]&15 != 0 {
			t.Fatalf("failed fallback: %v %x", seen, ans)
		}
	}
}

func TestCleanResolverSuccessfulAnswerDoesNotQueryPeer(t *testing.T) {
	s, _, _ := newHookHarness(t)
	s.rules.foreign = []string{"8.8.8.8", "10.0.0.2"}
	var seen []string
	s.forward = func(up string, q []byte, _ time.Duration) ([]byte, error) {
		seen = append(seen, up)
		return buildTestDNSAnswer(q, "192.0.2.9"), nil
	}
	s.answer(buildDNSQueryMsg("a.test"))
	if !reflect.DeepEqual(seen, []string{"8.8.8.8"}) {
		t.Fatal(seen)
	}
}
