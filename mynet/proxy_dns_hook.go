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
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
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

// ── PC 形态 DNS 答案钩子（local forwarder）───────────────────
// 以下为 2026-10-17 新增：mynet proxy dns-hook。
// 原 DynamicIPSet* 是网关侧原语，保留不动。

// DNSHookListenDefault PC 钩子默认监听。:53 才能被系统 DNS 直接指到。
const DNSHookListenDefault = "127.0.0.1:53"

// dnsHookPIDFile 常驻进程 pid。
func dnsHookPIDFile() string {
	return filepath.Join(varDir(), "proxy", "dns-hook.pid")
}

// dnsHookStateFile 运行状态（上游、启动时间）。
func dnsHookStateFile() string {
	return filepath.Join(varDir(), "proxy", "dns-hook.json")
}

// dnsHookPinFile 钩子增量钉扎清单（与 apply 的 pc-pin.json 分开：
// apply 对账会整表重写 pc-pin，钩子是并发增量写入者，必须独立文件再合并）。
func dnsHookPinFile() string {
	return filepath.Join(varDir(), "proxy", "dns-hook-pins.json")
}

type dnsHookPinState struct {
	mu    sync.Mutex
	IPs   map[string]time.Time `json:"ips"` // ip → first seen
	rules *dnsHookRules
}

// dnsHookRules 钩子侧规则快照（从 intent 读；运行中每 N 秒刷新）。
type dnsHookRules struct {
	mu      sync.RWMutex
	proxied map[string]bool // 归一化域名
	clean   map[string]bool
	// foreign 国外/代理域名上游：peer 出口 SmartDNS 优先（隧道内干净）。
	// 不塞 8.8.8.8——那也会绕隧道，不如直接问 peer。
	foreign []string
	// domestic 国内/其他域名上游：平台大陆 DNS + 公共国内 DNS，直连不进隧道。
	domestic []string
}

// onlyIPUpstreams 只保留 IP 形态的上游（用户裁决 2026-10-17：上游 DNS 必须是 IP）。
// 主机名上游（dns.dsplat.com / dns.mynet.club）在 hook 接管系统 DNS 后解析会
// 打回自己 → 死锁，所以构造期直接剔除；forwardDNSQuery 里仍留 bootstrap 兜底
// （配置里手写主机名时不至于全挂）。
func onlyIPUpstreams(list []string) []string {
	return filterIPUpstreams(list, false)
}

// filterIPUpstreams quiet=true 时不打「非 IP」日志（热刷新每 30s 会走一遍，
// 否则刷屏；丢弃原因由调用方在真正无后备时再记）。
func filterIPUpstreams(list []string, quiet bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		if net.ParseIP(s) == nil {
			if !quiet {
				hookLogf("上游 %q 非 IP，忽略（上游 DNS 必须为 IP）", s)
			}
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// ipUpstreamsOrResolve IP 优先；主机名 bootstrap 成 IP 作**后备**，不丢。
// 上游必须是 IP（不能走系统解析器），但**不能滤成空或只留一个死 IP**——
// 空表会让 proxied 域名零上游；只留出口 VIP 时对端 DNS 一挂就全断
// （2026-10-17 实测：github.com 走 dns-hook 超时）。
func ipUpstreamsOrResolve(list []string) []string {
	ips := filterIPUpstreams(list, true)
	var out []string
	seen := map[string]bool{}
	for _, ip := range ips {
		seen[ip] = true
		out = append(out, ip)
	}
	// 主机名 → bootstrap IP，挂在已 IP 之后作后备
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" || net.ParseIP(s) != nil {
			continue
		}
		ip := upstreamIPCacheGet(s)
		if ip == "" {
			ip = bootstrapResolveA(s, 2*time.Second)
			if ip == "" {
				hookLogf("上游 %q 既非 IP 也 bootstrap 失败，忽略", s)
				continue
			}
			hookLogf("上游 %q → %s（bootstrap）", s, ip)
		}
		if seen[ip] {
			continue
		}
		seen[ip] = true
		out = append(out, ip)
	}
	if len(out) == 0 {
		hookLogf("上游列表滤后为空（原 %v）——proxied 域名将无应答", list)
	}
	return out
}

// proxyExitVias 生效出口的 overlay VIP（foreign 首选：出口 VIP:53，proxy-split.md §13）。
// intent 无 exits 时从插件壳补（双轨合一读侧以 Effective 为准，这里兜底）。
func proxyExitVias(p *domain.ProxyPolicy) []string {
	var out []string
	seen := map[string]bool{}
	add := func(via string) {
		via = strings.TrimSpace(via)
		if via == "" || seen[via] || net.ParseIP(via) == nil {
			return
		}
		seen[via] = true
		out = append(out, via)
	}
	if p != nil {
		for _, e := range p.Exits {
			if e.Enabled {
				add(e.Via)
			}
		}
	}
	if len(out) == 0 {
		if base, _, err := activePluginBase(); err == nil {
			if pc, err := loadPluginAt(base, domain.PluginNameProxy); err == nil {
				if pp, err := domain.DecodePluginProxy(pc); err == nil {
					for _, e := range pp.EffectiveExits() {
						add(e.Via)
					}
				}
			}
		}
	}
	return out
}

// defaultForeignUpstreams 国外上游：TunnelServer/ExitDNS → 出口 VIP → 平台 DNS。
// 全部收敛成 IP（出口 VIP 本就是 IP；平台 DNS 主机名走 bootstrap）。
func defaultForeignUpstreams(p *domain.ProxyPolicy, rules *domain.ProxyDomainRules) []string {
	var out []string
	if rules != nil {
		if rules.TunnelServer != "" {
			out = append(out, rules.TunnelServer)
		}
		if rules.ExitDNS != "" && rules.ExitDNS != rules.TunnelServer {
			out = append(out, rules.ExitDNS)
		}
	}
	// 出口 peer VIP（设计：foreign 默认 = 出口 VIP:53）
	out = append(out, proxyExitVias(p)...)
	if rules != nil {
		// 平台 DNS 兜底（仍经隧道 IP 钉扎路径时可用）
		if pd := rules.EffectivePlatformDNS(); pd != "" {
			out = append(out, pd)
		}
	}
	if len(out) == 0 {
		out = []string{domain.PlatformDNSDefault}
	}
	return ipUpstreamsOrResolve(out)
}

// defaultDomesticUpstreams 国内上游：快、直连、不进隧道。
func defaultDomesticUpstreams(rules *domain.ProxyDomainRules) []string {
	var out []string
	if rules != nil && len(rules.UpstreamDirect) > 0 {
		out = append(out, rules.UpstreamDirect...)
	}
	// 平台大陆智能 DNS + 公共国内
	out = append(out, domain.PlatformDNSDomestic)
	out = append(out, domain.DefaultDomesticDNS...)
	// IP 优先；主机名 bootstrap 成 IP 作后备（不能滤成空）
	return ipUpstreamsOrResolve(out)
}

func newHookRulesFromPolicy() *dnsHookRules {
	r := &dnsHookRules{proxied: map[string]bool{}, clean: map[string]bool{}}
	p := EffectiveProxyPolicy()
	var dr *domain.ProxyDomainRules
	if p != nil && p.DomainRules != nil {
		dr = p.DomainRules
		for _, d := range dr.EffectiveProxied() {
			r.proxied[normalizeDomainName(d)] = true
		}
		for _, d := range dr.UpstreamClean {
			r.clean[normalizeDomainName(d)] = true
		}
	}
	r.foreign = defaultForeignUpstreams(p, dr)
	r.domestic = defaultDomesticUpstreams(dr)
	return r
}

func (r *dnsHookRules) shouldPin(qname string) bool {
	if r == nil {
		return false
	}
	d := normalizeDomainName(qname)
	if d == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return matchHookDomain(d, r.proxied) || matchHookDomain(d, r.clean)
}

// matchHookDomain 精确或后缀匹配；map 键可含 `*.` 前缀（归一化时已去）。
func matchHookDomain(d string, set map[string]bool) bool {
	if set[d] {
		return true
	}
	for item := range set {
		if item == "" {
			continue
		}
		// `*.example.com` 归一化后就是 `example.com`；后缀即子域
		if strings.HasSuffix(d, "."+item) {
			return true
		}
	}
	return false
}

// upstreamsFor 按域名选上游：代理/干净域名 → 国外（peer）；其他 → 国内（直连）。
func (r *dnsHookRules) upstreamsFor(qname string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.shouldPinLocked(qname) {
		if len(r.foreign) == 0 {
			// 上游必须是 IP，但不能滤成空——空表 = proxied 域名零上游、客户端超时。
			// 主机名走 bootstrap（不经系统解析器，无死锁）。
			return ipUpstreamsOrResolve([]string{domain.PlatformDNSDefault})
		}
		return append([]string{}, r.foreign...)
	}
	if len(r.domestic) == 0 {
		return onlyIPUpstreams(domain.DefaultDomesticDNS)
	}
	return append([]string{}, r.domestic...)
}

func (r *dnsHookRules) shouldPinLocked(qname string) bool {
	d := normalizeDomainName(qname)
	if d == "" {
		return false
	}
	return matchHookDomain(d, r.proxied) || matchHookDomain(d, r.clean)
}

// ── 最小 DNS 报文：抽 QNAME 与 A 答案 ──────────────────────────

// buildDNSQueryMsg 构造最小 A 查询报文（探活用）。
func buildDNSQueryMsg(qname string) []byte {
	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[0:2], 0x1234)
	binary.BigEndian.PutUint16(msg[2:4], 0x0100)
	binary.BigEndian.PutUint16(msg[4:6], 1)
	for _, part := range strings.Split(qname, ".") {
		msg = append(msg, byte(len(part)))
		msg = append(msg, part...)
	}
	msg = append(msg, 0)
	msg = append(msg, 0, 1, 0, 1) // A IN
	return msg
}

// parseDNSQName 解析报文 offset 处的域名标签，返回名与新 offset。
func parseDNSQName(msg []byte, offset int) (string, int, error) {
	var parts []string
	// 防压缩指针死循环
	for hops := 0; hops < 64; hops++ {
		if offset >= len(msg) {
			return "", 0, fmt.Errorf("qname truncated")
		}
		l := int(msg[offset])
		if l == 0 {
			return strings.Join(parts, "."), offset + 1, nil
		}
		if l&0xC0 == 0xC0 {
			if offset+1 >= len(msg) {
				return "", 0, fmt.Errorf("bad pointer")
			}
			ptr := int(binary.BigEndian.Uint16(msg[offset:offset+2]) & 0x3FFF)
			name, _, err := parseDNSQName(msg, ptr)
			if err != nil {
				return "", 0, err
			}
			if name != "" {
				parts = append(parts, name)
			}
			return strings.Join(parts, "."), offset + 2, nil
		}
		offset++
		if offset+l > len(msg) {
			return "", 0, fmt.Errorf("label overflow")
		}
		parts = append(parts, string(msg[offset:offset+l]))
		offset += l
	}
	return "", 0, fmt.Errorf("qname too deep")
}

// parseDNSQuestion 取查询名（跳过 id/flags/qdcount）。
func parseDNSQuestion(msg []byte) (string, error) {
	if len(msg) < 12 {
		return "", fmt.Errorf("short header")
	}
	qd := binary.BigEndian.Uint16(msg[4:6])
	if qd == 0 {
		return "", fmt.Errorf("no question")
	}
	name, off, err := parseDNSQName(msg, 12)
	if err != nil {
		return "", err
	}
	// 跳过 qtype/qclass
	if off+4 > len(msg) {
		return "", fmt.Errorf("short question tail")
	}
	return name, nil
}

// extractDNSAnswerAIDs 从完整应答报文抽 A 记录 IPv4（仅 Answer 段）。
func extractDNSAnswerAIDs(msg []byte) []string {
	if len(msg) < 12 {
		return nil
	}
	ancount := int(binary.BigEndian.Uint16(msg[6:8]))
	if ancount == 0 {
		return nil
	}
	// 跳 header + question(s)
	off := 12
	qd := int(binary.BigEndian.Uint16(msg[4:6]))
	for i := 0; i < qd; i++ {
		_, n, err := parseDNSQName(msg, off)
		if err != nil {
			return nil
		}
		off = n + 4
		if off > len(msg) {
			return nil
		}
	}
	var out []string
	for i := 0; i < ancount && off < len(msg); i++ {
		// NAME（可能是指针）
		if off+10 > len(msg) {
			break
		}
		if msg[off]&0xC0 == 0xC0 {
			off += 2
		} else {
			_, n, err := parseDNSQName(msg, off)
			if err != nil {
				break
			}
			off = n
		}
		if off+10 > len(msg) {
			break
		}
		typ := binary.BigEndian.Uint16(msg[off : off+2])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
		off += 10
		if off+rdlen > len(msg) {
			break
		}
		if typ == 1 && rdlen == 4 { // A
			ip := net.IP(msg[off : off+4]).String()
			out = append(out, ip)
		}
		off += rdlen
	}
	return out
}

// forwardDNSQuery 依次尝试上游，返回完整应答报文。
//
// ⚠️ 上游可能是主机名（dns.dsplat.com / dns.mynet.club）。**绝不能用系统解析器**
// 解开它 —— hook 已经接管系统 DNS，解析上游会再打回 hook 自己，死锁超时。
// （2026-10-17 实测：`dns apply 127.0.0.1` 保险丝② 检测 qq.com/baidu.com
// i/o timeout → 回滚，根因就是这条死锁链。）
// 所以主机名一律走 bootstrap 直连解析（打知名 IP 的 53，不经系统栈）。
func forwardDNSQuery(upstream string, query []byte, timeout time.Duration) ([]byte, error) {
	addr := upstream
	if net.ParseIP(upstream) == nil {
		ip := bootstrapResolveA(upstream, timeout)
		if ip == "" {
			return nil, fmt.Errorf("bootstrap 解析上游 %s 失败", upstream)
		}
		addr = ip
	}
	conn, err := net.DialTimeout("udp", net.JoinHostPort(addr, "53"), timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, 1232)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// bootstrapDNSes bootstrap 解析用的知名公网 DNS（直连 UDP:53，不经系统解析器）。
var bootstrapDNSes = []string{"223.5.5.5", "119.29.29.29", "8.8.8.8"}

// bootstrapResolveA 把主机名解析成 A 记录 IPv4。
// 实现：向 bootstrapDNSes 直接发一个 A 查询（buildDNSQueryMsg / extractDNSAnswerAIDs
// 已有现成编解码），**不调用 net.LookupIP**（那会走系统解析器 → 死锁）。
func bootstrapResolveA(host string, timeout time.Duration) string {
	// 已有缓存就直接用（上游主机名极少变）
	if ip := upstreamIPCacheGet(host); ip != "" {
		return ip
	}
	q := buildDNSQueryMsg(host)
	if len(q) == 0 {
		return ""
	}
	for _, dns := range bootstrapDNSes {
		ans, err := forwardUDP(dns, q, timeout)
		if err != nil {
			continue
		}
		ips := extractDNSAnswerAIDs(ans)
		if len(ips) > 0 && net.ParseIP(ips[0]) != nil {
			upstreamIPCachePut(host, ips[0])
			return ips[0]
		}
	}
	return ""
}

// forwardUDP 裸 UDP 查询（bootstrap 专用，不递归进 forwardDNSQuery）。
func forwardUDP(server string, query []byte, timeout time.Duration) ([]byte, error) {
	conn, err := net.DialTimeout("udp", net.JoinHostPort(server, "53"), timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, 1232)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// 上游主机名 → IP 的小缓存（进程内，够用；上游极少变）。
var upstreamIPCache sync.Map // host → {ip, expires}

type upstreamIPEntry struct {
	ip      string
	expires time.Time
}

func upstreamIPCacheGet(host string) string {
	if v, ok := upstreamIPCache.Load(host); ok {
		if e, ok2 := v.(upstreamIPEntry); ok2 && time.Now().Before(e.expires) {
			return e.ip
		}
		upstreamIPCache.Delete(host)
	}
	return ""
}

func upstreamIPCachePut(host, ip string) {
	upstreamIPCache.Store(host, upstreamIPEntry{ip: ip, expires: time.Now().Add(10 * time.Minute)})
}

// warmUpstreamIPs 启动时把上游列表里的主机名解析成 IP 进缓存（幂等、只补缺失）。
func warmUpstreamIPs(lists ...[]string) {
	for _, list := range lists {
		for _, up := range list {
			if up == "" || net.ParseIP(up) != nil {
				continue // IP 直接用
			}
			if upstreamIPCacheGet(up) != "" {
				continue
			}
			if ip := bootstrapResolveA(up, 3*time.Second); ip != "" {
				hookLogf("upstream %s → %s", up, ip)
			} else {
				hookLogf("upstream %s 解析失败（查询时会重试）", up)
			}
		}
	}
}

// ── 应答缓存（dns-hook 只做 forward，不缓存就每次全量打上游）────────
//
// 键 = qname + qtype；值 = 完整应答报文 + 过期时间。
// 命中时把报文前 2 字节的事务 ID 改写成当前查询的，再回给客户端 ——
// DNS 客户端只认 ID 配对，缓存响应必须重写 ID。
//
// TTL：优先吃应答里最小的 A 记录 TTL；没有 A 记录或解析不出 → 默认 30s，
// 上限 300s（防把 86400 这种长 TTL 钉死导致解析漂移后长期错误）。

const (
	dnsCacheDefaultTTL = 30 * time.Second
	dnsCacheMaxTTL     = 300 * time.Second
	dnsCacheMaxEntries = 4096
)

type dnsCacheEntry struct {
	resp    []byte
	expires time.Time
}

type dnsRespCache struct {
	mu      sync.Mutex
	entries map[string]dnsCacheEntry
}

func newDNSRespCache() *dnsRespCache {
	return &dnsRespCache{entries: map[string]dnsCacheEntry{}}
}

// key 查询缓存键（qname 归一化 + qtype）。
func dnsCacheKey(qname string, qtype uint16) string {
	return normalizeDomainName(qname) + "|" + strconv.FormatUint(uint64(qtype), 10)
}

// get 命中返回应答副本（ID 已用 queryID 改写）；未命中/过期返回 nil。
func (c *dnsRespCache) get(key string, queryID uint16) []byte {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		if ok {
			delete(c.entries, key)
		}
		return nil
	}
	out := append([]byte{}, e.resp...)
	if len(out) >= 2 {
		binary.BigEndian.PutUint16(out[0:2], queryID)
	}
	return out
}

// put 写入应答；ttl<=0 用默认。超容量时清掉已过期项，仍满则丢弃新增。
func (c *dnsRespCache) put(key string, resp []byte, ttl time.Duration) {
	if c == nil || len(resp) < 12 {
		return
	}
	if ttl <= 0 {
		ttl = dnsCacheDefaultTTL
	}
	if ttl > dnsCacheMaxTTL {
		ttl = dnsCacheMaxTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= dnsCacheMaxEntries {
		now := time.Now()
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= dnsCacheMaxEntries {
			return
		}
	}
	c.entries[key] = dnsCacheEntry{
		resp:    append([]byte{}, resp...),
		expires: time.Now().Add(ttl),
	}
}

// minAnswerTTL 取应答段 A 记录的最小 TTL；没有 A 记录返回 0（调用方用默认）。
func minAnswerTTL(msg []byte) time.Duration {
	if len(msg) < 12 {
		return 0
	}
	ancount := int(binary.BigEndian.Uint16(msg[6:8]))
	if ancount == 0 {
		return 0
	}
	off := 12
	qd := int(binary.BigEndian.Uint16(msg[4:6]))
	for i := 0; i < qd; i++ {
		_, n, err := parseDNSQName(msg, off)
		if err != nil {
			return 0
		}
		off = n + 4
		if off > len(msg) {
			return 0
		}
	}
	minTTL := uint32(0)
	for i := 0; i < ancount && off < len(msg); i++ {
		if off+10 > len(msg) {
			break
		}
		if msg[off]&0xC0 == 0xC0 {
			off += 2
		} else {
			_, n, err := parseDNSQName(msg, off)
			if err != nil {
				break
			}
			off = n
		}
		if off+10 > len(msg) {
			break
		}
		typ := binary.BigEndian.Uint16(msg[off : off+2])
		ttl := binary.BigEndian.Uint32(msg[off+4 : off+8])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
		off += 10
		if off+rdlen > len(msg) {
			break
		}
		if typ == 1 { // A
			if minTTL == 0 || ttl < minTTL {
				minTTL = ttl
			}
		}
		off += rdlen
	}
	if minTTL == 0 {
		return 0
	}
	return time.Duration(minTTL) * time.Second
}

// queryType 取查询的 qtype（header+qname 之后的 2 字节）。
func queryType(msg []byte) uint16 {
	if len(msg) < 14 {
		return 0
	}
	_, off, err := parseDNSQName(msg, 12)
	if err != nil || off+2 > len(msg) {
		return 0
	}
	return binary.BigEndian.Uint16(msg[off : off+2])
}

// hookLogf 钩子日志（stderr；常驻时由调用方重定向）。
var hookLogf = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dns-hook: "+format+"\n", args...)
}

// runDNSHookServer 前台跑 forwarder（阻塞）。stop 由信号/CLI pid kill。
func runDNSHookServer(listen string, done <-chan struct{}) error {
	rules := newHookRulesFromPolicy()
	pins := &dnsHookPinState{IPs: map[string]time.Time{}, rules: rules}
	cache := newDNSRespCache()

	// 规则热刷新（intent 可能被 localapi 改）
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fresh := newHookRulesFromPolicy()
				rules.mu.Lock()
				rules.proxied = fresh.proxied
				rules.clean = fresh.clean
				rules.foreign = fresh.foreign
				rules.domestic = fresh.domestic
				rules.mu.Unlock()
			}
		}
	}()

	pc, err := net.ListenPacket("udp", listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listen, err)
	}
	defer pc.Close()
	hookLogf("listening %s foreign=%v domestic=%v", listen, rules.foreign, rules.domestic)

	// 启动即预解析上游主机名（dns.dsplat.com / dns.mynet.club）→ IP，
	// 进 bootstrap 缓存。不预热的话第一次查询要现场解析，且若此时
	// 系统 DNS 已指向本 hook，会自己查自己（死锁）。
	// PlatformDNS 主机名也预热：foreign 可能只有出口 VIP，兜底路径还要用它。
	// **必须放后台**：bootstrap 每个主机名最长 3s，同步跑会卡住 ReadFrom，
	// 父进程 2s 探活窗口一到就 SIGKILL（2026-10-17 实测「端口未就绪」）。
	go warmUpstreamIPs(rules.foreign, rules.domestic,
		[]string{domain.PlatformDNSDefault, domain.PlatformDNSDomestic})

	// 持久化已加载
	pins.load()

	go func() {
		<-done
		_ = pc.Close()
	}()

	buf := make([]byte, 2048)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if done != nil {
				select {
				case <-done:
					return nil
				default:
				}
			}
			return err
		}
		query := append([]byte{}, buf[:n]...)
		go handleHookQuery(pc, addr, query, rules, pins, cache)
	}
}

func handleHookQuery(pc net.PacketConn, addr net.Addr, query []byte, rules *dnsHookRules, pins *dnsHookPinState, cache *dnsRespCache) {
	qname, err := parseDNSQuestion(query)
	if err != nil {
		return
	}
	// 缓存命中：改写事务 ID 后直接回，不打上游（DNS 客户端只认 ID 配对）。
	qtype := queryType(query)
	if len(query) >= 2 {
		queryID := binary.BigEndian.Uint16(query[0:2])
		if hit := cache.get(dnsCacheKey(qname, qtype), queryID); hit != nil {
			_, _ = pc.WriteTo(hit, addr)
			return
		}
	}
	var answer []byte
	for _, up := range rules.upstreamsFor(qname) {
		if ans, err2 := forwardDNSQuery(up, query, 4*time.Second); err2 == nil && len(ans) >= 12 {
			answer = ans
			break
		}
	}
	if answer == nil {
		return // 无应答；客户端自行超时
	}
	// 应答进缓存：TTL 吃 A 记录的最小值，无 A 记录用默认 30s（上限 300s）。
	cache.put(dnsCacheKey(qname, qtype), answer, minAnswerTTL(answer))
	// 增量钉扎：**只管加速域名**（proxied/clean），不碰国内域名（用户裁决 2026-10-17）。
	// 流程：干净 DNS 解析 → CIDR 感知查表 → 已覆盖则不做任何操作，
	// 未覆盖才补装路由（随后客户端请求自然进隧道）。
	if rules.shouldPin(qname) {
		for _, ip := range extractDNSAnswerAIDs(answer) {
			if net.ParseIP(ip) == nil {
				continue
			}
			// 已被现有前缀覆盖 或 FIB 已走隧道 → 不进清单、不碰路由
			if ipCoveredByPinCIDRs(ip) || ipRoutedViaTun(ip, hookTunDevice()) {
				continue
			}
			if pins.add(ip) {
				hookLogf("pin %s ← %s", ip, qname)
				pinOneIP(ip)
			}
		}
	}
	_, _ = pc.WriteTo(answer, addr)
}

// add 新 IP → true（需要装路由）；已存在 → false。
func (s *dnsHookPinState) add(ip string) bool {
	if net.ParseIP(ip) == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.IPs[ip]; ok {
		return false
	}
	s.IPs[ip] = time.Now()
	s.persistLocked()
	return true
}

func (s *dnsHookPinState) load() {
	raw, err := os.ReadFile(dnsHookPinFile())
	if err != nil {
		return
	}
	var body struct {
		IPs map[string]time.Time `json:"ips"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.IPs == nil {
		// 兼容 {"ips":["a","b"]} 形状
		var alt struct {
			IPs []string `json:"ips"`
		}
		if err2 := json.Unmarshal(raw, &alt); err2 == nil {
			for _, ip := range alt.IPs {
				s.IPs[ip] = time.Time{}
			}
		}
		return
	}
	s.IPs = body.IPs
}

func (s *dnsHookPinState) persistLocked() {
	p := dnsHookPinFile()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	raw, _ := json.Marshal(map[string]any{"ips": s.IPs})
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err == nil {
		_ = os.Rename(tmp, p)
	}
}

// LoadDNSHookPins 读钩子增量钉扎清单（IP 列表）。
func LoadDNSHookPins() []string {
	raw, err := os.ReadFile(dnsHookPinFile())
	if err != nil {
		return nil
	}
	var body struct {
		IPs map[string]time.Time `json:"ips"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && body.IPs != nil {
		out := make([]string, 0, len(body.IPs))
		for ip := range body.IPs {
			out = append(out, ip)
		}
		return out
	}
	var alt struct {
		IPs []string `json:"ips"`
	}
	if err := json.Unmarshal(raw, &alt); err == nil {
		return alt.IPs
	}
	return nil
}

// pinOneIP 把新 IP 并入钩子清单后，**重算最小合并集**再装/撤（失败只告警）。
// 相邻 IP 会收成 /31 等，避免主表被 /32 刷满。
//
// 钉扎前先做 **CIDR 感知的路由表判定**（2026-10-17 用户裁决）：
//   - IP 已被某条前缀覆盖且走隧道 → 不做任何操作，DNS 应答直接回；
//   - 否则才补装路由（再发起请求时流量自然进隧道）。
//
// 这样主表不会被「/32 刷满」，也不会对已覆盖前缀重复写 route replace。
func pinOneIP(ip string) {
	if net.ParseIP(ip) == nil {
		return
	}
	snap, err := shared.ReadSnapshot(shared.DefaultSnapshotPath())
	if err != nil || snap == nil || !isPCProxyForm(snap) {
		return
	}
	tun := hookTunDevice()
	if tun == "" {
		return
	}
	// 已被现有前缀（本钩子 / apply 的合并集）覆盖 → 静默跳过
	if ipCoveredByPinCIDRs(ip) {
		return
	}
	// FIB 最长前缀匹配：已有更宽路由把它送进隧道 → 也跳过
	if ipRoutedViaTun(ip, tun) {
		return
	}
	// 未覆盖：并入清单后重算最小合并集再装（失败只告警）
	all := LoadDNSHookPins()
	merged := MergeIPsToCIDR(all)
	cmds := PCProxyPinCmds(snap, merged, tun)
	for _, c := range cmds {
		_ = runOp(false, c.argv)
	}
}

// ipCoveredByPinCIDRs IP 是否已落在本机已钉前缀（dns-hook-pins + pc-pin）内。
// 钩子增量写 dns-hook-pins，apply 对账写 pc-pin；两份都要看。
//
// 判定来源两类：
//  1. 清单里的 **CIDR 条目**（如 10.1.2.0/24）—— 直接 Contains；
//  2. 清单里的 **裸 IP** —— 先 MergeIPsToCIDR 收成最小前缀，再 Contains
//     （相邻 .4+.5 → /31，新来 .4 就不必再补装）。
func ipCoveredByPinCIDRs(ip string) bool {
	target := net.ParseIP(ip)
	if target == nil {
		return false
	}
	for _, src := range [][]string{LoadDNSHookPins(), LoadPCPinIPs()} {
		var bare []string
		for _, entry := range src {
			if _, n, err := net.ParseCIDR(entry); err == nil {
				if n.Contains(target) {
					return true
				}
				continue
			}
			bare = append(bare, entry)
		}
		for _, c := range MergeIPsToCIDR(bare) {
			_, n, err := net.ParseCIDR(c)
			if err == nil && n.Contains(target) {
				return true
			}
		}
	}
	return false
}

// ipRoutedViaTun FIB 查询：IP 的最长前缀匹配是否已走隧道口。
// BSD 用 `route -n get`，Linux 用 `ip route get`；拿不准时返回 false（补装，保守）。
func ipRoutedViaTun(ip, tun string) bool {
	if tun == "" {
		return false
	}
	var out []byte
	if runtime.GOOS == "windows" {
		// Windows 查 route print 开销大；先用 pin 集判定，FIB 留给 apply 对账。
		return false
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "freebsd" {
		b, err := exec.Command("route", "-n", "get", ip).Output()
		if err != nil {
			return false
		}
		out = b
	} else {
		b, err := exec.Command("ip", "route", "get", ip).Output()
		if err != nil {
			return false
		}
		out = b
	}
	text := string(out)
	// darwin: `interface: utun4`；linux: `dev utun4`
	if strings.Contains(text, tun) {
		return true
	}
	return false
}

// hookTunDevice 从 bundle 反推隧道口；失败返回空。
func hookTunDevice() string {
	env, err := loadBundleEnvelope()
	if err != nil || env == nil {
		return ""
	}
	dev, err := env.TunDevice()
	if err != nil {
		return ""
	}
	snap, _ := shared.ReadSnapshot(shared.DefaultSnapshotPath())
	if snap == nil {
		return dev
	}
	return resolveDevice(dev, env.Network.Addresses)
}

// ── CLI ────────────────────────────────────────────────────────

func cmdProxyDNSHook(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet proxy dns-hook start|stop|status [--listen ADDR]")
		return 2
	}
	switch args[0] {
	case "start":
		listen := DNSHookListenDefault
		for i := 1; i < len(args); i++ {
			if args[i] == "--listen" && i+1 < len(args) {
				listen = args[i+1]
				i++
			}
		}
		// 用户显式开启 → 记住偏好，隧道重启后 SyncSidecarsOnTunnelUp 会跟上。
		if rc := dnsHookStart(listen); rc != 0 {
			return rc
		}
		_ = SetDNSHookWanted(true)
		return 0
	case "stop":
		// 用户显式关闭 → 清偏好（与隧道联动停不同：联动停不改意图）。
		if rc := dnsHookStop(); rc != 0 {
			return rc
		}
		_ = SetDNSHookWanted(false)
		return 0
	case "status":
		return dnsHookStatus()
	default:
		fmt.Fprintf(os.Stderr, "dns-hook: 未知子命令 %q\n", args[0])
		return 2
	}
}

func dnsHookStart(listen string) int {
	if err := os.MkdirAll(filepath.Dir(dnsHookPIDFile()), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// 子进程：先写自己的 pid，再起 server（避免与父进程抢写）
	if os.Getenv("MYNET_DNS_HOOK_CHILD") == "1" {
		_ = os.WriteFile(dnsHookPIDFile(), []byte(strconv.Itoa(os.Getpid())), 0o644)
		done := make(chan struct{})
		go func() {
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
			<-ch
			close(done)
		}()
		if err := runDNSHookServer(listen, done); err != nil {
			hookLogf("server exit: %v", err)
			return 1
		}
		return 0
	}
	// 父进程：已运行则提示
	if pid := readHookPID(); pid > 0 && processAlive(pid) {
		fmt.Printf("dns-hook 已在运行 pid=%d\n", pid)
		return 0
	}
	// 清掉死 pid 文件
	_ = os.Remove(dnsHookPIDFile())
	// :53 被其他进程占用 → 拒绝启动（不劫持别人的 DNS）
	if occupied := portOccupied(listen); occupied {
		fmt.Fprintf(os.Stderr, "dns-hook: %s 已被其他程序占用，拒绝启动 DNS 劫持\n", listen)
		fmt.Fprintln(os.Stderr, "  处置：停掉占用方后重试，或换 --listen 127.0.0.1:5353 并手动把系统 DNS 指过去")
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Setsid：脱离当前终端；stderr 丢到 hook log
	logf, _ := os.OpenFile(filepath.Join(varDir(), "proxy", "dns-hook.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	var files []*os.File
	if logf != nil {
		files = []*os.File{nil, logf, logf}
	} else {
		files = []*os.File{nil, os.Stderr, os.Stderr}
	}
	attr := &os.ProcAttr{
		Files: files,
		Env:   append(os.Environ(), "MYNET_DNS_HOOK_CHILD=1"),
		Sys:   &syscall.SysProcAttr{Setsid: true},
	}
	proc, err := os.StartProcess(exe, []string{exe, "proxy", "dns-hook", "start", "--listen", listen}, attr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start child:", err)
		if logf != nil {
			_ = logf.Close()
		}
		return 1
	}
	// 子进程自己写 pid；父进程稍候确认
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if pid := readHookPID(); pid > 0 && processAlive(pid) {
			// 探活 :53
			if probeLocalForwarder(listen) == nil {
				return dnsHookAfterStart(listen, pid)
			}
		}
		if !processAlive(proc.Pid) {
			break
		}
	}
	if logf != nil {
		_ = logf.Close()
	}
	fmt.Fprintln(os.Stderr, "dns-hook 启动失败或端口未就绪（查 var/proxy/dns-hook.log；:53 需 root）")
	_ = syscall.Kill(proc.Pid, syscall.SIGKILL)
	return 1
}

// probeLocalForwarder 对本地 forwarder 发一条 A 查询，必须有应答。
func probeLocalForwarder(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "53"
	}
	// 探活只要 forwarder 能回任意 DNS 应答即可。用国内名走 domestic 上游——
	// connectivitycheck.gstatic.com 在部分网络下上游全挂会超时，把已就绪的
	// hook 误判成「端口未就绪」并 SIGKILL（2026-10-17 实测）。
	q := buildDNSQueryMsg("baidu.com")
	conn, err := net.DialTimeout("udp", net.JoinHostPort(host, port), 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(q); err != nil {
		return err
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil || n < 12 {
		if err == nil {
			err = fmt.Errorf("short reply %d", n)
		}
		return err
	}
	return nil
}

func dnsHookAfterStart(listen string, pid int) int {
	// 接管系统 DNS → 127.0.0.1（仅当监听 :53）
	host, _, _ := net.SplitHostPort(listen)
	if host == "127.0.0.1" || host == "localhost" || host == "" {
		if err := ApplySystemDNS("", "127.0.0.1"); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ 系统 DNS 接管失败（forwarder 已起 pid=%d）: %v\n", pid, err)
			fmt.Fprintln(os.Stderr, "  可手动: sudo mynet dns apply 127.0.0.1")
		} else {
			fmt.Println("✓ 系统 DNS → 127.0.0.1（forwarder 钩子）")
		}
	}
	fmt.Printf("✓ dns-hook 已启动 pid=%d listen=%s\n", pid, listen)
	return 0
}

func dnsHookStop() int {
	pid := readHookPID()
	if pid <= 0 {
		fmt.Println("dns-hook 未运行")
		// 仍尝试还原 DNS
		_ = RestoreSystemDNS()
		return 0
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	time.Sleep(200 * time.Millisecond)
	if processAlive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	_ = os.Remove(dnsHookPIDFile())
	_ = RestoreSystemDNS()
	fmt.Println("✓ dns-hook 已停止，系统 DNS 已还原")
	return 0
}

func dnsHookStatus() int {
	pid := readHookPID()
	alive := pid > 0 && processAlive(pid)
	fmt.Printf("dns-hook: pid=%d alive=%v listen_default=%s pins=%d\n",
		pid, alive, DNSHookListenDefault, len(LoadDNSHookPins()))
	return 0
}

func readHookPID() int {
	raw, err := os.ReadFile(dnsHookPIDFile())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return n
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// portOccupied 尝试绑定 UDP 端口判断是否被占；成功即释放。
// 返回 true = 已有别人（或已死的残留）在听。
func portOccupied(listen string) bool {
	pc, err := net.ListenPacket("udp", listen)
	if err != nil {
		return true
	}
	_ = pc.Close()
	return false
}
