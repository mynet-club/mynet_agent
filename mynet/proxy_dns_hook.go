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
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
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

// dnsHookRules 钩子侧规则快照（从 intent 读；运行中每 N 秒刷新）。
type dnsHookRules struct {
	mu      sync.RWMutex
	proxied map[string]bool // 归一化域名
	// foreign 国外/代理域名上游：peer 出口 SmartDNS 优先（隧道内干净）。
	// 不塞 8.8.8.8——那也会绕隧道，不如直接问 peer。
	foreign        []string
	direct         map[string]bool
	customIPs      []string
	signature      string
	block          *dnsAdBlock
	blockSignature string
	upstreamError  error
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

// ipUpstreamsOrResolve IP 优先，主机名命中 bootstrap 缓存则用 IP，否则保留给查询路径解析。
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
			ip = s
		} // 未预热时保留端点，查询时独立 bootstrap，构造规则不等网络。
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

// Fallback only when neither local nor tenant/policy DNS is configured.
// 兜底 = 公共干净 DNS 内置清单（与 ctl dns-clean.txt 同源的一份两用清单）。
// 2026-10-28：原 dnsHookPreferredCleanDNS=221.216.138.69（天津 ISP）实测投毒
// （t66y.com → 104.244.4x.x 假 IP），已整链摘除，兜底改走 builtinCleanDNSList。

func defaultForeignUpstreams(p *domain.ProxyPolicy, rules *domain.ProxyDomainRules) []string {
	upstreams, _ := hookForeignUpstreams(p, rules)
	return upstreams
}

// defaultDomesticUpstreams 国内上游：快、直连、不进隧道。
func defaultDomesticUpstreams(rules *domain.ProxyDomainRules) []string {
	var out []string
	if rules != nil && len(rules.UpstreamDirect) > 0 {
		out = append(out, rules.UpstreamDirect...)
	}
	// 公共国内 DNS。2026-10-28 摘除 PlatformDNSDomestic（dns.dsplat.com）：
	// 实测 CNAME→home.luoyueliang.com→221.216.138.69（投毒 ISP DNS），
	// 待平台侧修正指向后再评估恢复。
	out = append(out, domain.DefaultDomesticDNS...)
	// IP 优先；主机名 bootstrap 成 IP 作后备（不能滤成空）
	return ipUpstreamsOrResolve(out)
}

func newHookRulesFromPolicy() *dnsHookRules {
	r := &dnsHookRules{proxied: map[string]bool{}, direct: map[string]bool{}}
	// 拦截名单必须进规则快照：优先级 拦截>直连>加速>放行（shouldPin 顺序）。
	if b, sig, err := loadDNSAdBlock(); err == nil && b != nil {
		r.block, r.blockSignature = b, sig
	}
	p := EffectiveProxyPolicy()
	raw, _ := json.Marshal(p)
	r.signature = string(raw)
	var dr *domain.ProxyDomainRules
	if p != nil && p.Role != domain.ProxyRoleServer && p.DomainRules != nil {
		pcRules := *p.DomainRules
		// 三名单优先级（钉死）：拦截 > 直连 > 加速 > 放行。
		// 云端 direct-domains.txt 与本机 CustomDirect 合并为「直连」，
		// 均可排除加速白名单；冲突时直连压加速，拦截压一切（shouldPin 顺序）。
		dr = &pcRules
		r.customIPs = append([]string{}, dr.CustomIPs...)
		for _, d := range dr.EffectiveDirect() {
			r.direct[normalizeDomainName(d)] = true
		}
		for _, d := range dr.EffectiveProxied() {
			r.proxied[normalizeDomainName(d)] = true
		}
	}
	r.foreign, r.upstreamError = hookForeignUpstreams(p, dr)
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
	return !r.block.blocked(d) && !matchHookDomain(d, r.direct) && matchHookDomain(d, r.proxied)
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

// upstreamsFor 按白名单选上游：命中 → 出口；未命中 → 普通直连上游。
func (r *dnsHookRules) upstreamsFor(qname string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.shouldPinLocked(qname) {
		if len(r.foreign) == 0 {
			// Keep the same clean primary even for an incomplete rule snapshot.
			return append([]string{}, builtinCleanDNSList...)
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
	return !r.block.blocked(d) && !matchHookDomain(d, r.direct) && matchHookDomain(d, r.proxied)
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
	next, size := -1, 0
	for hops := 0; hops < 128; hops++ {
		if offset < 0 || offset >= len(msg) {
			return "", 0, fmt.Errorf("qname truncated")
		}
		l := int(msg[offset])
		if l == 0 {
			if next < 0 {
				next = offset + 1
			}
			return strings.Join(parts, "."), next, nil
		}
		if l&0xc0 == 0xc0 {
			if offset+1 >= len(msg) {
				return "", 0, fmt.Errorf("bad pointer")
			}
			if next < 0 {
				next = offset + 2
			}
			offset = int(binary.BigEndian.Uint16(msg[offset:offset+2]) & 0x3fff)
			continue
		}
		if l > 63 || offset+1+l > len(msg) {
			return "", 0, fmt.Errorf("label overflow")
		}
		size += l + 1
		if size > 254 {
			return "", 0, fmt.Errorf("qname too long")
		}
		offset++
		parts = append(parts, string(msg[offset:offset+l]))
		offset += l
	}
	return "", 0, fmt.Errorf("qname compression loop")
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
	stored  time.Time
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
	resp, _ := c.getWithExpiry(key, queryID)
	return resp
}

func (c *dnsRespCache) getWithExpiry(key string, queryID uint16) ([]byte, time.Time) {
	if c == nil {
		return nil, time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		if ok {
			delete(c.entries, key)
		}
		return nil, time.Time{}
	}
	out := append([]byte{}, e.resp...)
	if len(out) >= 2 {
		binary.BigEndian.PutUint16(out[0:2], queryID)
		ageDNSResponse(out, uint32(time.Since(e.stored)/time.Second))
	}
	return out, e.expires
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
		stored:  time.Now(),
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
// dnsHookServer 的策略锁避免旧上游查询在规则刷新后重新写回旧缓存/租约。
type dnsHookServer struct {
	gate    sync.RWMutex
	rules   *dnsHookRules
	pins    *pcProxySession
	cache   *dnsRespCache
	forward func(string, []byte, time.Duration) ([]byte, error)
	tun     func() string
}

func (s *dnsHookServer) refresh(fresh *dnsHookRules, now time.Time) error {
	if fresh.upstreamError != nil {
		return fmt.Errorf("DNS config (retaining previous rules): %w", fresh.upstreamError)
	}
	block, sig, err := loadDNSAdBlock()
	if err != nil {
		return fmt.Errorf("ad-block config (retaining previous rules): %w", err)
	}
	fresh.mu.RLock()
	copyRules := &dnsHookRules{
		proxied: fresh.proxied, direct: fresh.direct, customIPs: fresh.customIPs,
		foreign: fresh.foreign, domestic: fresh.domestic, signature: fresh.signature,
	}
	fresh.mu.RUnlock()
	fresh = copyRules
	fresh.block, fresh.blockSignature = block, sig
	s.gate.Lock()
	defer s.gate.Unlock()
	changed := fresh.blockSignature != s.rules.blockSignature || fresh.signature != s.rules.signature || !reflect.DeepEqual(fresh.foreign, s.rules.foreign) || !reflect.DeepEqual(fresh.domestic, s.rules.domestic)
	if changed {
		// 上游/规则变化时，旧应答不能再为新规则授权。域名租约在同一临界区清理。
		s.cache = newDNSRespCache()
		if !reflect.DeepEqual(fresh.foreign, s.rules.foreign) {
			s.pins.mu.Lock()
			s.pins.Domains = map[string]map[string]time.Time{}
			s.pins.mu.Unlock()
		}
		s.rules = fresh
	}
	return s.pins.sync(s.rules, s.tun(), now)
}

func runDNSHookServer(listen string, done <-chan struct{}) error {
	rules := newHookRulesFromPolicy()
	if rules.upstreamError != nil {
		return fmt.Errorf("DNS config: %w", rules.upstreamError)
	}
	block, blockSignature, err := loadDNSAdBlock()
	if err != nil {
		return fmt.Errorf("ad-block config: %w", err)
	}
	rules.block, rules.blockSignature = block, blockSignature
	pins := newPCProxySession()
	if err := pins.load(hookTunDevice()); err != nil {
		return err
	}
	server := &dnsHookServer{rules: rules, pins: pins, cache: newDNSRespCache(), forward: forwardDNSQuery, tun: liveHookTunDevice}
	pc, err := net.ListenPacket("udp", listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listen, err)
	}
	defer pc.Close()
	var workers sync.WaitGroup
	stop := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		close(stop)
		workers.Wait() // 不允许在释放后有查询重新装路由。
		if err := pins.release(time.Now()); err != nil {
			hookLogf("release: %v", err)
		}
	}()
	// 加速运行期间每 15s 探测出口（PC 单出口），状态落盘供 status/API 读取。
	startExitProbeLoop(stop)
	hookLogf("listening %s foreign=%v domestic=%v", listen, rules.foreign, rules.domestic)
	host, port, _ := net.SplitHostPort(listen)
	if port == "53" && (host == "127.0.0.1" || host == "localhost" || host == "") {
		workers.Add(1)
		events := hookDNSChangeEvents(ctx)
		go func() {
			defer workers.Done()
			monitorHookDNS(ctx.Done(), events, func() (bool, error) {
				if !hookDNSMonitorReady() || hookStopRequested() {
					return true, nil
				}
				owned, err := hookDNSOwned(LoadDNSBackup(), hookSystemDNSOps(), effectiveHookDNSServers)
				saveHookDNSObservation(owned, err)
				return owned, err
			}, func() error {
				if hookStopRequested() {
					return nil
				}
				return recordHookDNSInterruption()
			}, func() { _ = pc.Close() })
		}()
	}
	workers.Add(1)
	go func() { defer workers.Done(); warmUpstreamIPs(rules.foreign, rules.domestic) }()
	workers.Add(1)
	go func() {
		defer workers.Done()
		s := server
		s.updateBaseline(ctx)
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				s.updateBaseline(ctx)
			}
		}
	}()
	workers.Add(1)
	go func() {
		defer workers.Done()
		if err := server.refresh(rules, time.Now()); err != nil {
			hookLogf("restore: %v", err)
		}
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if liveHookTunDevice() == "" {
					_ = pc.Close()
					return
				}
				if err := server.refresh(newHookRulesFromPolicy(), time.Now()); err != nil {
					hookLogf("reconcile: %v", err)
				}
			}
		}
	}()
	go func() {
		select {
		case <-done:
			_ = pc.Close()
		case <-stop:
		}
	}()
	buf := make([]byte, 65535)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			select {
			case <-done:
				return nil
			default:
				return err
			}
		}
		query := append([]byte{}, buf[:n]...)
		workers.Add(1)
		go func() { defer workers.Done(); _, _ = pc.WriteTo(server.answer(query), addr) }()
	}
}

func (s *dnsHookServer) answer(query []byte) []byte {
	qname, err := parseDNSQuestion(query)
	if err != nil {
		return nil
	}
	s.gate.RLock()
	defer s.gate.RUnlock()
	if s.rules.block.blocked(qname) {
		return dnsBlockedAnswer(query)
	}
	key := dnsCacheKey(qname, queryType(query))
	answer, expires := s.cache.getWithExpiry(key, binary.BigEndian.Uint16(query[:2]))
	cached := answer != nil
	if answer == nil {
		for _, up := range s.rules.upstreamsFor(qname) {
			ans, err := s.forward(up, query, 4*time.Second)
			if err == nil && len(ans) >= 12 && ans[2]&0x80 != 0 && binary.BigEndian.Uint16(ans[:2]) == binary.BigEndian.Uint16(query[:2]) {
				// A reachable but unhealthy resolver must also fall back to the peer.
				rcode := ans[3] & 15
				if rcode == 2 || rcode == 5 {
					continue
				}
				answer = ans
				break
			}
		}
	}
	if answer == nil {
		return dnsFailure(query)
	}
	if s.rules.block.blockedAlias(answer) {
		return dnsBlockedAnswer(query)
	}
	// 返回给系统的 TTL 同样受上限约束，避免 OS 缓存比路由租约更长。
	ttl := limitDNSResponseTTL(answer, uint32(dnsCacheMaxTTL/time.Second))
	ips := extractDNSAnswerAIDs(answer)
	if !s.rules.shouldPin(qname) {
		// 普通域名解析到已被钉的 IP → 共享 CDN，改直连（避免劫持普通应用）
		if len(ips) > 0 {
			s.pins.markSharedIfPinned(ips)
		}
		return answer
	}
	if s.rules.shouldPin(qname) {
		if len(ips) > 0 {
			tun := s.tun()
			if tun == "" {
				return dnsFailure(query)
			}
			// TTL=0 的回答不缓存；用一秒短租约保证本次连接有机会使用路由。
			leaseTTL := ttl
			if leaseTTL <= 0 {
				leaseTTL = time.Second
			}
			now := time.Now()
			if !cached {
				expires = now.Add(leaseTTL)
			}
			if err := s.pins.observe(qname, ips, expires, now, s.rules, tun); err != nil {
				hookLogf("query %s: %v", qname, err)
				return dnsFailure(query)
			}
			remaining := time.Until(expires) / time.Second
			if remaining < 0 {
				remaining = 0
			}
			ttl = limitDNSResponseTTL(answer, uint32(remaining))
		}
	}
	if !cached && ttl > 0 && answer[3]&15 == 0 && answer[2]&2 == 0 {
		s.cache.put(key, answer, ttl)
	}
	return answer
}

func dnsFailure(query []byte) []byte {
	if len(query) < 12 {
		return nil
	}
	out := append([]byte{}, query...)
	out[2] = (out[2] & 1) | 0x80
	out[3] = 0x82 // RA + SERVFAIL；明确失败，不静默放行代理域名。
	for i := 6; i < 12; i++ {
		out[i] = 0
	}
	return out
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
	internal := os.Getenv("MYNET_DNS_HOOK_CHILD") == "1" || os.Getenv("MYNET_DNS_HOOK_WATCH") == "1"
	if !internal && len(args) > 0 && (args[0] == "start" || args[0] == "stop" || (args[0] == "ad-block" || args[0] == "dns") && len(args) > 1 && args[1] != "status") {
		return withDNSHookControl(func() int { return cmdProxyDNSHookUnlocked(args) })
	}
	return cmdProxyDNSHookUnlocked(args)
}

func cmdProxyDNSHookUnlocked(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet proxy dns-hook start|stop|status [--listen ADDR] | domains status|refresh | direct-ip add|del|list | ad-block on|off|status")
		return 2
	}
	switch args[0] {
	case "dns":
		return cmdHookDNSConfig(args[1:])
	case "domains":
		return cmdProxyDNSHookDomains(args[1:])
	case "direct-ip":
		return cmdProxyDNSHookDirectIPs(args[1:])
	case "ad-block":
		return cmdDNSAdBlock(args[1:])
	case "start":
		listen := DNSHookListenDefault
		for i := 1; i < len(args); i++ {
			if args[i] == "--listen" && i+1 < len(args) {
				listen = args[i+1]
				i++
			}
		}
		if os.Getenv("MYNET_DNS_HOOK_CHILD") == "1" || os.Getenv("MYNET_DNS_HOOK_WATCH") == "1" {
			return dnsHookStartUnlocked(listen)
		}
		if !isPCDNSPlatform() {
			fmt.Fprintln(os.Stderr, "dns-hook requires pc runtime mode")
			return 1
		}
		// 先保存统一偏好，watcher 在首次退出时也能读到用户选择。
		// 外部改写只允许用户显式重新接管；先收掉旧 worker 再清中断标记。
		if hookDNSInterrupted() {
			if rc := dnsHookStopUnlocked(); rc != 0 {
				return rc
			}
			if err := clearHookDNSInterruption(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		if err := SetDNSHookWanted(true); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := accelerationStartGate(); err != nil {
			fmt.Println("✓ 已记住加速偏好，" + err.Error())
			return 0
		}
		// 先更新驱动出口投影，再启动 DNS，避免仅启动了一个无策略的转发器。
		if err := ApplyFromActive(false); err != nil {
			fmt.Fprintln(os.Stderr, "加速策略准备失败:", err)
			return 1
		}
		if err := dnsHookStartGate(); err != nil {
			fmt.Println("✓ 加速节点已就绪，" + err.Error())
			return 0
		}
		return dnsHookStartUnlocked(listen)
	case "stop":
		preserveIntent := len(args) > 1 && args[1] == "--preserve-intent"
		if !preserveIntent {
			if err := SetDNSHookWanted(false); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		if rc := dnsHookStopUnlocked(); rc != 0 {
			return rc
		}
		if !preserveIntent && liveHookTunDevice() != "" {
			if err := ApplyFromActive(false); err != nil {
				fmt.Fprintln(os.Stderr, "加速关闭投影失败:", err)
				return 1
			}
		}
		if !preserveIntent {
			if err := clearHookDNSInterruption(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		return 0
	case "status":
		return dnsHookStatus()
	default:
		fmt.Fprintf(os.Stderr, "dns-hook: 未知子命令 %q\n", args[0])
		return 2
	}
}

func dnsHookStart(listen string) int {
	return withDNSHookControl(func() int { return dnsHookStartUnlocked(listen) })
}

func dnsHookStartUnlocked(listen string) int {
	if err := hookDNSResumeAllowed(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(dnsHookPIDFile()), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// 子进程：先写自己的 pid，再起 server（避免与父进程抢写）
	if os.Getenv("MYNET_DNS_HOOK_CHILD") == "1" {
		_ = os.WriteFile(dnsHookPIDFile(), []byte(strconv.Itoa(os.Getpid())), 0o644)
		done := make(chan struct{})
		monitorDone := make(chan struct{})
		defer close(monitorDone)
		go func() {
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(ch)
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-monitorDone:
					return
				case <-ch:
					close(done)
					return
				case <-tick.C:
					if hookStopRequested() {
						close(done)
						return
					}
				}
			}
		}()
		if err := runDNSHookServer(listen, done); err != nil {
			hookLogf("server exit: %v", err)
			return 1
		}
		return 0
	}
	if os.Getenv("MYNET_DNS_HOOK_WATCH") == "1" {
		return watchDNSHook(listen)
	}
	if !isPCDNSPlatform() {
		fmt.Fprintln(os.Stderr, "dns-hook requires pc runtime mode")
		return 1
	}
	// 父进程：已运行则提示
	if pid := readHookPID(); pid > 0 && processAlive(pid) {
		fmt.Printf("dns-hook 已在运行 pid=%d\n", pid)
		return 0
	}
	if hookWatcherAlive() {
		fmt.Fprintln(os.Stderr, "dns-hook 正在恢复或退出，请稍后重试")
		return 1
	}
	// 清掉已结束会话的 stop 请求
	_ = os.Remove(dnsHookStopFile())
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
		Env:   append(os.Environ(), "MYNET_DNS_HOOK_WATCH=1"),
		Sys:   hookDetachedAttr(),
	}
	if logf != nil {
		defer logf.Close()
	}
	proc, err := os.StartProcess(exe, []string{exe, "proxy", "dns-hook", "start", "--listen", listen}, attr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start child:", err)
		if logf != nil {
			_ = logf.Close()
		}
		return 1
	}
	defer proc.Release()
	// 子进程自己写 pid；父进程稍候确认
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if pid := readHookPID(); pid > 0 && processAlive(pid) {
			// 探活 :53
			if probeLocalForwarder(listen) == nil {
				rc := dnsHookAfterStart(listen, pid)
				if rc != 0 {
					_ = dnsHookStopUnlocked()
				}
				return rc
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
	_ = os.WriteFile(dnsHookStopFile(), []byte("stop"), 0600)
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
	host, port, _ := net.SplitHostPort(listen)
	if port == "53" && (host == "127.0.0.1" || host == "localhost" || host == "") {
		if err := ApplySystemDNS("", "127.0.0.1"); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ 系统 DNS 接管失败（forwarder 已起 pid=%d）: %v\n", pid, err)
			return 1
		} else {
			fmt.Println("✓ 系统 DNS → 127.0.0.1（forwarder 钩子）")
		}
	}
	if err := os.WriteFile(hookDNSReadyFile(), []byte(strconv.Itoa(pid)), 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("✓ dns-hook 已启动 pid=%d listen=%s\n", pid, listen)
	return 0
}

func dnsHookStop() int { return withDNSHookControl(dnsHookStopUnlocked) }

func dnsHookStopUnlocked() int {
	if err := os.MkdirAll(filepath.Dir(dnsHookStopFile()), 0755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(dnsHookStopFile(), []byte("stop"), 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	deadline := time.Now().Add(20 * time.Second)
	for (dnsHookRunning() || hookWatcherAlive()) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if dnsHookRunning() {
		pid := readHookPID()
		if !hookWorkerOwned(pid) {
			fmt.Fprintln(os.Stderr, "dns-hook pid 与执行器命令不匹配，拒绝终止其它进程")
			return 1
		}
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
		// watcher Wait 后负责释放；给它明确的退出窗口。
		deadline = time.Now().Add(10 * time.Second)
		for hookWatcherAlive() && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if hookWatcherAlive() {
		fmt.Fprintln(os.Stderr, "dns-hook 清理尚未完成")
		return 1
	}
	if err := cleanupDNSHook(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = os.Remove(dnsHookPIDFile())
	_ = os.Remove(dnsHookStopFile())
	fmt.Println("✓ dns-hook 已停止，路由已释放、系统 DNS 已还原")
	return 0
}

func dnsHookStopFile() string  { return filepath.Join(varDir(), "proxy", "dns-hook.stop") }
func dnsHookWatchFile() string { return filepath.Join(varDir(), "proxy", "dns-hook-watch.pid") }
func hookStopRequested() bool  { _, err := os.Stat(dnsHookStopFile()); return err == nil }
func hookWatcherAlive() bool {
	raw, _ := os.ReadFile(dnsHookWatchFile())
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return pid > 0 && processAlive(pid)
}

func cleanupDNSHook() error {
	_ = os.Remove(hookDNSReadyFile())
	_ = os.Remove(hookDNSObservationFile())
	ClearExitProbeResult()
	// DNS 优先恢复，路由清理失败不能把系统解析留在死端口。
	dnsErr := restoreHookDNS()
	pins := newPCProxySession()
	if err := pins.load(hookTunDevice()); err != nil {
		return errors.Join(dnsErr, err)
	}
	return errors.Join(dnsErr, pins.release(time.Now()))
}

// watcher 仅监督这一个 worker：正常停、panic、强杀都先恢复 DNS/释放路由。
// 意图仍开启且隧道存在时重启；用户停或隧道联动停通过 stop 文件明确终止。
func watchDNSHook(listen string) int {
	if err := os.WriteFile(dnsHookWatchFile(), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return 1
	}
	defer os.Remove(dnsHookWatchFile())
	exe, err := os.Executable()
	if err != nil {
		return 1
	}
	for attempt := 0; ; attempt++ {
		worker := exec.Command(exe, "proxy", "dns-hook", "start", "--listen", listen)
		worker.Env = append(os.Environ(), "MYNET_DNS_HOOK_CHILD=1")
		worker.Stdout, worker.Stderr = os.Stdout, os.Stderr
		if err := worker.Start(); err != nil {
			hookLogf("worker start: %v", err)
			return 1
		}
		waited := make(chan error, 1)
		go func() { waited <- worker.Wait() }()
		if attempt > 0 {
			ready := false
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) && !hookStopRequested() {
				if probeLocalForwarder(listen) == nil {
					ready = true
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !ready || hookStopRequested() || dnsHookAfterStart(listen, worker.Process.Pid) != 0 {
				_ = worker.Process.Kill()
				<-waited
				if err := cleanupDNSHook(); err != nil {
					hookLogf("restart cleanup: %v", err)
				}
				_ = os.Remove(dnsHookPIDFile())
				return 1
			}
		}
		err := <-waited
		if raw, rerr := os.ReadFile(hookDNSReadyFile()); rerr == nil && strings.TrimSpace(string(raw)) == strconv.Itoa(worker.Process.Pid) && !hookStopRequested() {
			owned, checkErr := hookDNSOwned(LoadDNSBackup(), hookSystemDNSOps(), effectiveHookDNSServers)
			if checkErr == nil && !owned {
				_ = recordHookDNSInterruption()
			}
		}
		if err != nil {
			hookLogf("worker exited: %v", err)
		}
		if err := cleanupDNSHook(); err != nil {
			hookLogf("cleanup: %v", err)
			return 1
		}
		_ = os.Remove(dnsHookPIDFile())
		if hookStopRequested() || hookDNSInterrupted() || !DNSHookWanted() || liveHookTunDevice() == "" {
			return 0
		}
		// 重启也走启动门禁：节点不可达 / DNS 不可用时不拉起，等下次侧车或用户重试。
		if err := accelerationStartGate(); err != nil {
			hookLogf("restart deferred: %v", err)
			return 0
		}
		if err := dnsHookStartGate(); err != nil {
			hookLogf("restart deferred: %v", err)
			return 0
		}
		time.Sleep(time.Second)
		if hookStopRequested() {
			return 0
		}
	}
}

func dnsHookStatus() int {
	pid := readHookPID()
	alive := pid > 0 && processAlive(pid)
	pins := newPCProxySession()
	if err := pins.load(hookTunDevice()); err != nil {
		return 1
	}
	state := accelerationStateReady(DNSHookWanted(), alive, liveHookTunDevice() != "",
		accelerationNodeReady(), dnsResolutionReady())
	if alive {
		switch hookDNSObservation(pid) {
		case "unknown":
			state = "dns_unknown"
		case "overridden":
			state = "dns_overridden"
		}
	}
	if hookDNSInterrupted() && DNSHookWanted() {
		state = "dns_overridden"
	}
	active := alive && state == "running"
	fmt.Printf("dns-hook: pid=%d alive=%v wanted=%v state=%s active=%v listen_default=%s pins=%d domains=%d routes=%d\n",
		pid, alive, DNSHookWanted(), state, active, DNSHookListenDefault, len(LoadDNSHookPins()), len(pins.Domains), len(pins.Routes))
	// 出口探测（加速启用时每 15s）；未启用/无出口时输出空目标行。
	probe, probeOK := LoadExitProbeResult()
	if !alive || !DNSHookWanted() {
		probeOK = false
	}
	fmt.Println(FormatExitProbeStatus(probe, probeOK))
	if pins.LastError != "" {
		fmt.Printf("last_error=%s\n", pins.LastError)
	}
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

// 控制面只串行启停，不重试抢锁；防止两个 watcher 同时恢复/覆盖系统 DNS。
func withDNSHookControl(action func() int) int {
	p := filepath.Join(varDir(), "proxy", "dns-hook-control.lock")
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer f.Close()
	if err := lockDNSHookControl(f); err != nil {
		fmt.Fprintln(os.Stderr, "dns-hook 另一个启停操作正在执行:", err)
		return 1
	}
	return action()
}

// accelerationState 区分等待连接、等待加速节点、等待 DNS 和已就绪却未启动。
func accelerationState(wanted, alive, tunnel bool) string {
	return accelerationStateReady(wanted, alive, tunnel, true, true)
}

// accelerationStateReady 在 accelerationState 基础上区分启动门禁：
// 隧道已在但加速节点未达 → waiting_node；节点已达但 DNS 不可用 → waiting_dns。
func accelerationStateReady(wanted, alive, tunnel, nodeReady, dnsReady bool) string {
	if alive {
		return "running"
	}
	if !wanted {
		return "disabled"
	}
	if !tunnel {
		return "waiting_tunnel"
	}
	if !nodeReady {
		return "waiting_node"
	}
	if !dnsReady {
		return "waiting_dns"
	}
	return "failed"
}
