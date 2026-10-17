package mynet

// PC 形态域名解析：走**干净上游**（隧道/出口 DNS）+ 磁盘缓存。
//
// 为什么不能用系统解析器：
//   - 系统 DNS 走公网 UDP:53，可被链路劫持/污染 → 钉进隧道的是污染 IP，代理形同虚设；
//   - 干净上游 = TunnelServer（peer VIP，查询自动进隧道）> ExitDNS > 平台 DNS 兜底。
//
// 缓存：$MYNET_HOME/var/proxy/pc-dns-cache.json。域名→IPs + 解析时间。
// TTL 内直接命中磁盘缓存，不发查询；过期才重解析。失败保留旧缓存（fail-open 到旧 IP），
// 但清空时返回空集 —— 宁可少钉，不钉错。

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// pcDNSCacheTTL 域名解析缓存时长。CDN IP 短命，取 1h 折中。
const pcDNSCacheTTL = time.Hour

// pcDNSTimeout 单域名干净解析超时。
const pcDNSTimeout = 5 * time.Second

type pcDNSCacheEntry struct {
	IPs        []string  `json:"ips"`
	ResolvedAt time.Time `json:"resolved_at"`
}

type pcDNSCacheFile struct {
	Entries map[string]pcDNSCacheEntry `json:"entries"`
}

func pcDNSCachePath() string {
	return filepath.Join(varDir(), "proxy", "pc-dns-cache.json")
}

// LoadPCDNSCache 读缓存；缺失/损坏 → 空表（不报错）。
func LoadPCDNSCache() map[string]pcDNSCacheEntry {
	out := map[string]pcDNSCacheEntry{}
	raw, err := os.ReadFile(pcDNSCachePath())
	if err != nil {
		return out
	}
	var f pcDNSCacheFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Entries == nil {
		return out
	}
	return f.Entries
}

// SavePCDNSCache 写缓存（原子：tmp + rename）。
func SavePCDNSCache(entries map[string]pcDNSCacheEntry) error {
	p := pcDNSCachePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(pcDNSCacheFile{Entries: entries})
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// CleanDNSUpstreams 干净上游有序列表：TunnelServer > ExitDNS > PlatformDNS。
// 空列表 = 无可用干净上游（调用方回落系统解析器并告警）。
func CleanDNSUpstreams(rules *domain.ProxyDomainRules) []string {
	if rules == nil {
		return nil
	}
	var out []string
	if rules.TunnelServer != "" {
		out = append(out, rules.TunnelServer)
	}
	if rules.ExitDNS != "" {
		out = append(out, rules.ExitDNS)
	}
	// 平台 DNS 兜底：公网 UDP:53，不保证干净，但比系统 ISP DNS 好一档。
	out = append(out, rules.EffectivePlatformDNS())
	return out
}

// cleanLookupV4 经指定上游查 A 记录。上游不可达/超时返回 nil。
func cleanLookupV4(server, domain string) []string {
	if server == "" || domain == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), pcDNSTimeout)
	defer cancel()
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: pcDNSTimeout}
			return d.DialContext(ctx, "udp", net.JoinHostPort(server, "53"))
		},
	}
	addrs, err := r.LookupIPAddr(ctx, domain)
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		if v4 := a.IP.To4(); v4 != nil {
			out = append(out, v4.String())
		}
	}
	return out
}

// ResolveDomainClean 带缓存的干净解析：TTL 命中 → 磁盘；否则逐上游试，成功即缓存。
// 全部上游失败：有旧缓存则沿用（fail-open），无缓存返回空。
func ResolveDomainClean(rules *domain.ProxyDomainRules, domain string, now time.Time) []string {
	d := normalizeDomainName(domain)
	if d == "" {
		return nil
	}
	cache := LoadPCDNSCache()
	if e, ok := cache[d]; ok && now.Sub(e.ResolvedAt) < pcDNSCacheTTL && len(e.IPs) > 0 {
		return append([]string{}, e.IPs...)
	}
	for _, up := range CleanDNSUpstreams(rules) {
		if ips := cleanLookupV4(up, d); len(ips) > 0 {
			cache[d] = pcDNSCacheEntry{IPs: ips, ResolvedAt: now}
			_ = SavePCDNSCache(cache)
			return append([]string{}, ips...)
		}
	}
	// 全失败：旧缓存仍可用则沿用（不刷新时间，下次 apply 再试）。
	if e, ok := cache[d]; ok && len(e.IPs) > 0 {
		return append([]string{}, e.IPs...)
	}
	return nil
}
