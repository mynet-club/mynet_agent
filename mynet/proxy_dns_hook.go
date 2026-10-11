package mynet

// DNS 回答钩子：proxied 域名的 A 记录 → 动态 ipset（proxy-split.md §3.2 步骤 4）。
//
// 与静态段表（chinaip/interip 全量装载）分开：
//   - 静态：启动时 flush+load（WriteNftSetFragment）
//   - 动态：增量 add，**绝不 flush**，否则段表被清掉
//
// 网关优先用 dnsmasq 原生 `ipset=/domain/set`（解析即入集）；
// PC/无 dnsmasq 时由调用方把 (domain, IPs) 喂给 DynamicIPSet。

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mynet-club/mynet_agent/shared"
)

// DynamicIPSet 域名解析出的动态 IP（带 TTL）。
type DynamicIPSet struct {
	mu sync.Mutex
	// entries ip → 过期时间
	entries map[string]time.Time
	// domains 域名 → 最近一次解析的 IP 集合（便于整体过期）
	domains map[string][]string
	// TTL 默认存活时间
	TTL time.Duration
}

func NewDynamicIPSet(ttl time.Duration) *DynamicIPSet {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &DynamicIPSet{
		entries: map[string]time.Time{},
		domains: map[string][]string{},
		TTL:     ttl,
	}
}

// AddAnswer 记录一次 DNS 回答。返回**新增**的 IP（已存在不重复报）。
func (s *DynamicIPSet) AddAnswer(domain string, ips []string, now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(now)
	d := normalizeDomainName(domain)
	exp := now.Add(s.TTL)
	var added []string
	for _, raw := range ips {
		ip := strings.TrimSpace(raw)
		if net.ParseIP(ip) == nil {
			continue
		}
		if _, ok := s.entries[ip]; !ok {
			added = append(added, ip)
		}
		s.entries[ip] = exp
	}
	if d != "" {
		s.domains[d] = append([]string{}, ips...)
	}
	return added
}

// Expire 清掉过期 IP，返回被删的 IP。
func (s *DynamicIPSet) Expire(now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expireLocked(now)
}

func (s *DynamicIPSet) expireLocked(now time.Time) []string {
	var dead []string
	for ip, exp := range s.entries {
		if now.After(exp) {
			dead = append(dead, ip)
			delete(s.entries, ip)
		}
	}
	sort.Strings(dead)
	return dead
}

// IPs 当前有效 IP 列表（排序）。
func (s *DynamicIPSet) IPs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.entries))
	for ip := range s.entries {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

// Flush 清空（停用代理时）。
func (s *DynamicIPSet) Flush() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.IPs()
	s.entries = map[string]time.Time{}
	s.domains = map[string][]string{}
	return out
}

// DynamicIPSetCmds 增量写入 set 的命令。
//
// set 已由静态装载建好；这里只 add 元素，**禁止 flush**。
func DynamicIPSetCmds(snap *shared.SystemSnapshot, ips []string, nftSet, nftKey, ipsetName string) []routeCmd {
	if len(ips) == 0 {
		return nil
	}
	if nftSet == "" {
		nftSet = "mynet_proxy"
	}
	if nftKey == "" {
		nftKey = "mynet_proxy"
	}
	if ipsetName == "" {
		ipsetName = "mynet_proxy"
	}
	var cmds []routeCmd
	if snap.Capabilities["nft"] {
		for _, ip := range ips {
			cmds = append(cmds, routeCmd{
				argv: []string{
					"nft", "add", "element", "ip", nftSet, nftKey, "{", ip, "}",
				},
				optional: true,
			})
		}
		return cmds
	}
	if snap.Capabilities["iptables"] {
		for _, ip := range ips {
			cmds = append(cmds, routeCmd{
				argv:     []string{"ipset", "add", ipsetName, ip, "-exist"},
				optional: true,
			})
		}
		return cmds
	}
	return nil
}

// DynamicIPSetDelCmds 过期 IP 的删除命令。
func DynamicIPSetDelCmds(snap *shared.SystemSnapshot, ips []string, nftSet, nftKey, ipsetName string) []routeCmd {
	if len(ips) == 0 {
		return nil
	}
	if nftSet == "" {
		nftSet = "mynet_proxy"
	}
	if nftKey == "" {
		nftKey = "mynet_proxy"
	}
	if ipsetName == "" {
		ipsetName = "mynet_proxy"
	}
	var cmds []routeCmd
	if snap.Capabilities["nft"] {
		for _, ip := range ips {
			cmds = append(cmds, routeCmd{
				argv:     []string{"nft", "delete", "element", "ip", nftSet, nftKey, "{", ip, "}"},
				optional: true,
			})
		}
		return cmds
	}
	if snap.Capabilities["iptables"] {
		for _, ip := range ips {
			cmds = append(cmds, routeCmd{
				argv:     []string{"ipset", "del", ipsetName, ip},
				optional: true,
			})
		}
		return cmds
	}
	return nil
}

// DnsmasqIPSetLines dnsmasq 原生联动：解析即把 A 记录打进 ipset。
//
//	ipset=/google.com/mynet_proxy
//
// 网关主路径；不需要我们自己解析 DNS 包。
func DnsmasqIPSetLines(domains []string, setName string) []string {
	if setName == "" {
		setName = "mynet_proxy"
	}
	seen := map[string]bool{}
	var lines []string
	for _, raw := range domains {
		d := normalizeDomainName(raw)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		lines = append(lines, fmt.Sprintf("ipset=/%s/%s", d, setName))
	}
	sort.Strings(lines)
	return lines
}
