package mynet

// PC 动态路由只有 dns-hook 一个写入者。持久化租约用于恢复，Routes 是
// 本功能的路由所有权日志；白名单很大也只加载实际查询过且未过期的地址。
import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

type pcProxySession struct {
	mu      sync.Mutex
	Domains map[string]map[string]time.Time `json:"domains"`
	Routes  map[string]string               `json:"routes"` // prefix → tunnel interface
	// IPs 保留读面兼容；值是租约截止时间，不是 first-seen。
	IPs              map[string]time.Time `json:"ips"`
	LastError        string               `json:"last_error,omitempty"`
	explicitPrefixes []string             // 当前活跃的显式静态前缀（预设 ∪ custom_ips），不受 TTL 与共享降级影响
	route            func(action, prefix, tun string) error
	inspect          func() (pcRouteView, error)
	localNets        func() []*net.IPNet
	save             func() error
}

func newPCProxySession() *pcProxySession {
	s := &pcProxySession{
		Domains:   map[string]map[string]time.Time{},
		Routes:    map[string]string{},
		IPs:       map[string]time.Time{},
		localNets: localConnectedNets,
	}
	s.route = func(action, prefix, tun string) error {
		snap, err := shared.ReadSnapshot(shared.DefaultSnapshotPath())
		if err != nil {
			return err
		}
		if !isPCProxyForm(snap) {
			return fmt.Errorf("DNS hook requires pc runtime mode")
		}
		for _, c := range routeCmds(snap, RouteOp{Action: action, Destination: prefix, Device: tun}) {
			if err := runOp(false, c.argv); err != nil && !c.optional {
				return err
			}
		}
		return nil
	}
	s.inspect = readPCRoutes
	lastSaved := ""
	s.save = func() error {
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		if string(raw) == lastSaved {
			return nil
		}
		p := dnsHookPinFile()
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(p+".tmp", raw, 0600); err != nil {
			return err
		}
		if err := os.Rename(p+".tmp", p); err != nil {
			return err
		}
		lastSaved = string(raw)
		return nil
	}
	return s
}

// load 只恢复有归属的租约。旧 IP 清单无法推导域名，作为待释放路由迁移，
// 不把旧地址永久重新授权；下一次真实查询自然重新建立租约。
func (s *pcProxySession) load(tun string) error {
	raw, err := os.ReadFile(dnsHookPinFile())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(raw) > 0 {
		var disk struct {
			Domains   map[string]map[string]time.Time `json:"domains"`
			Routes    map[string]string               `json:"routes"`
			LastError string                          `json:"last_error,omitempty"`
			IPs       json.RawMessage                 `json:"ips"`
		}
		if err := json.Unmarshal(raw, &disk); err != nil {
			return err
		}
		s.LastError = disk.LastError
		if disk.Domains != nil {
			s.Domains = disk.Domains
		}
		if disk.Routes != nil {
			s.Routes = disk.Routes
		} else if tun != "" {
			var ips map[string]time.Time
			var list []string
			if json.Unmarshal(disk.IPs, &ips) == nil {
				for ip := range ips {
					list = append(list, ip)
				}
			} else {
				_ = json.Unmarshal(disk.IPs, &list)
			}
			for _, prefix := range MergeIPsToCIDR(list) {
				s.Routes[prefix] = tun
			}
			// 旧实现曾同时留下 /32 和合并前缀，都必须精确清理。
			for _, prefix := range normalizePinDests(list) {
				s.Routes[prefix] = tun
			}
		}
	}
	if tun != "" {
		for _, prefix := range normalizePinDests(LoadPCPinIPs()) {
			s.Routes[prefix] = tun
		}
	}
	return nil
}

// activeLocked 计算当前活跃集合：动态 DNS 租约（带 TTL）与显式静态前缀（预设 ∪ custom_ips）。
// 显式前缀永不因 TTL 过期，也不被 net.ParseIP 丢弃。
func (s *pcProxySession) activeLocked(rules *dnsHookRules, now time.Time) (dynamic map[string]time.Time, explicit []string) {
	dynamic = map[string]time.Time{}
	for name, ips := range s.Domains {
		if !rules.shouldPin(name) {
			delete(s.Domains, name)
			continue
		}
		for ip, until := range ips {
			if !until.After(now) {
				delete(ips, ip)
				continue
			}
			if dynamic[ip].Before(until) {
				dynamic[ip] = until
			}
		}
		if len(ips) == 0 {
			delete(s.Domains, name)
		}
	}
	explicit = rules.explicitPrefixes()
	return dynamic, explicit
}

func countActiveEntries(dynamic map[string]time.Time, explicit []string) int {
	count := len(explicit)
	for ip := range dynamic {
		if !domain.IPv4PrefixContainsAny(explicit, ip) {
			count++
		}
	}
	return count
}

// domainLastUsed 返回域名下所有 IP 中最晚的租约截止时间（用于 LRU 淘汰排序）。
func domainLastUsed(ips map[string]time.Time) time.Time {
	var latest time.Time
	for _, exp := range ips {
		if exp.After(latest) {
			latest = exp
		}
	}
	return latest
}

// evictOldestDomain 淘汰最久未使用的动态域名（排除 keepName），并重算 activeLocked。
func (s *pcProxySession) evictOldestDomain(keepName string, rules *dnsHookRules, now time.Time) (dynamic map[string]time.Time, explicit []string, evicted bool) {
	oldestName := ""
	var oldestTime time.Time
	for name, ips := range s.Domains {
		if name == keepName {
			continue
		}
		last := domainLastUsed(ips)
		if oldestName == "" || last.Before(oldestTime) {
			oldestName = name
			oldestTime = last
		}
	}
	if oldestName == "" {
		dynamic, explicit = s.activeLocked(rules, now)
		return dynamic, explicit, false
	}
	delete(s.Domains, oldestName)
	dynamic, explicit = s.activeLocked(rules, now)
	return dynamic, explicit, true
}

// observe 写入或刷新域名的路由钉扎租约；满容量时按 LRU 淘汰最久未使用的动态域名。
func (s *pcProxySession) observe(name string, ips []string, until, now time.Time, rules *dnsHookRules, tun string) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if err != nil {
			s.LastError = err.Error()
			_ = s.save()
		}
	}()
	name = normalizeDomainName(name)
	if !rules.shouldPin(name) {
		return nil
	}
	next := map[string]time.Time{}
	for ip, exp := range s.Domains[name] {
		next[ip] = exp
	}
	for _, ip := range ips {
		v4 := net.ParseIP(ip).To4()
		if v4 == nil {
			continue
		}
		ip = v4.String()
		if next[ip].Before(until) {
			next[ip] = until
		}
	}
	if len(next) > 0 {
		s.Domains[name] = next
	}
	dynamic, explicit := s.activeLocked(rules, now)
	for {
		// 直连 IP 豁免仅作用于动态 DNS pin，不降级显式 IP/CIDR。
		for ip := range dynamic {
			if directIPExempted(ip) && !domain.IPv4PrefixContainsAny(explicit, ip) {
				delete(dynamic, ip)
			}
		}
		if len(s.Domains) <= pcPinMaxIPs && countActiveEntries(dynamic, explicit) <= pcPinMaxIPs {
			break
		}
		var evicted bool
		dynamic, explicit, evicted = s.evictOldestDomain(name, rules, now)
		if !evicted {
			return fmt.Errorf("active IP limit %d reached", pcPinMaxIPs)
		}
	}
	return s.syncLocked(rules, tun, now)
}

func (s *pcProxySession) sync(rules *dnsHookRules, tun string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncLocked(rules, tun, now)
}

func (s *pcProxySession) syncLocked(rules *dnsHookRules, tun string, now time.Time) (err error) {
	defer func() {
		s.LastError = ""
		if err != nil {
			s.LastError = err.Error()
		}
		if saveErr := s.save(); err == nil {
			err = saveErr
		}
	}()
	dynamic, explicit := s.activeLocked(rules, now)
	// 直连 IP 豁免仅作用于动态 DNS pin，不影响显式 IP/CIDR 前缀。
	for ip := range dynamic {
		if directIPExempted(ip) && !domain.IPv4PrefixContainsAny(explicit, ip) {
			delete(dynamic, ip)
		}
	}
	if countActiveEntries(dynamic, explicit) > pcPinMaxIPs {
		return fmt.Errorf("active IP limit %d reached", pcPinMaxIPs)
	}
	s.explicitPrefixes = explicit
	// 读面兼容：IPs 记录活跃动态 IP 与显式前缀（显式项时间置零）。
	combined := make(map[string]time.Time, len(dynamic)+len(explicit))
	for ip, exp := range dynamic {
		combined[ip] = exp
	}
	for _, prefix := range explicit {
		combined[prefix] = time.Time{}
	}
	s.IPs = combined
	// 无待装/待撤路由时无需读取系统表（正常关闭、未访问任何域名）。
	if len(combined) == 0 && len(s.Routes) == 0 {
		return nil
	}
	view, err := s.inspect()
	if err != nil {
		return err
	}
	want := map[string]bool{}
	if tun != "" {
		var locals []*net.IPNet
		if s.localNets != nil {
			locals = s.localNets()
		}
		// 1) 显式静态前缀分轨：/32 单 IP 并入 MergeIPsToCIDR 保持相邻紧凑合并；
		//    /<N>（N<32）网段直接作为前缀进 want，跳过与本机直连网段重叠的条目。
		var singleIPs []string
		var widePrefixes []string
		for _, prefix := range explicit {
			if overlapsAny(parseDestNet(prefix), locals) {
				hookLogf("skip explicit prefix %s: overlaps local connected subnet", prefix)
				continue
			}
			if strings.HasSuffix(prefix, "/32") {
				singleIPs = append(singleIPs, strings.TrimSuffix(prefix, "/32"))
			} else {
				widePrefixes = append(widePrefixes, prefix)
				want[prefix] = true
			}
		}
		// 2) 动态 DNS IP：若已被更宽的显式静态前缀覆盖，无需再装重复 /32。
		for ip := range dynamic {
			if domain.IPv4PrefixContainsAny(widePrefixes, ip) {
				continue
			}
			singleIPs = append(singleIPs, ip)
		}
		for _, prefix := range MergeIPsToCIDR(singleIPs) {
			want[prefix] = true
		}
		// 3) 干净 DNS（1.1.1.1/8.8.8.8…）**永久钉入**：查询它们必须走隧道，
		// 否则 GFW 投毒；不参与租约过期，也不被共享 IP 解钉。
		for _, c := range loadCleanDNSList() {
			if ip := net.ParseIP(c); ip != nil {
				want[ip.String()+"/32"] = true
			}
		}
	}
	// 先持久化新增租约；失败时不改系统路由。
	if err := s.save(); err != nil {
		return err
	}
	// 预计算待增与待删集合；先做冲突校验，避免半装或误删上一代路由。
	var toDel []string
	toDelSet := map[string]bool{}
	for prefix, prevTun := range s.Routes {
		if want[prefix] && prevTun == tun {
			continue
		}
		toDel = append(toDel, prefix)
		toDelSet[prefix] = true
	}
	sort.Strings(toDel)
	// remainingView 排除即将删除的自管旧路由，避免缩容（如 /31 → /32）时被待删旧路由误判为已被覆盖。
	remainingView := make(pcRouteView, len(view))
	for p, dev := range view {
		if toDelSet[p] && s.Routes[p] == dev {
			continue
		}
		remainingView[p] = dev
	}
	var toAdd []string
	for prefix := range want {
		if s.Routes[prefix] == tun && view[prefix] == tun {
			continue
		}
		if remainingView.covers(prefix, tun) {
			continue
		}
		if dev, exists := view[prefix]; exists && dev != tun {
			if s.Routes[prefix] != dev {
				return fmt.Errorf("route %s belongs to %s", prefix, dev)
			}
		}
		toAdd = append(toAdd, prefix)
	}
	sort.Strings(toAdd)
	// 先撤换口/旧接口冲突的同名路由（不同接口不能共存同一前缀），
	// 再新增目标前缀；若新增中途失败，回滚本轮已新增项并保留其余旧路由。
	var samePrefixRebind []string
	var remainingDel []string
	for _, prefix := range toDel {
		if want[prefix] {
			samePrefixRebind = append(samePrefixRebind, prefix)
		} else {
			remainingDel = append(remainingDel, prefix)
		}
	}
	for _, prefix := range samePrefixRebind {
		prevTun := s.Routes[prefix]
		if view[prefix] == prevTun {
			if err := s.route("del", prefix, prevTun); err != nil {
				return fmt.Errorf("release %s: %w", prefix, err)
			}
			delete(view, prefix)
		}
		delete(s.Routes, prefix)
	}
	var added []string
	for _, prefix := range toAdd {
		if remainingView.covers(prefix, tun) {
			continue
		}
		s.Routes[prefix] = tun
		// 写前记所有权，进程突然退出后仍可精确清理。
		if err := s.save(); err != nil {
			return err
		}
		if err := s.route("add", prefix, tun); err != nil {
			// 回滚本轮之前已成功新增的前缀，保留上一代路由（不允许半装）。
			for i := len(added) - 1; i >= 0; i-- {
				p := added[i]
				_ = s.route("del", p, tun)
				delete(s.Routes, p)
				delete(view, p)
				delete(remainingView, p)
			}
			return fmt.Errorf("pin %s: %w", prefix, err)
		}
		view[prefix] = tun
		remainingView[prefix] = tun
		added = append(added, prefix)
	}
	// 所有新增成功后再清理废弃旧前缀。
	for _, prefix := range remainingDel {
		prevTun := s.Routes[prefix]
		if view[prefix] == prevTun {
			if err := s.route("del", prefix, prevTun); err != nil {
				return fmt.Errorf("release %s: %w", prefix, err)
			}
		}
		delete(s.Routes, prefix)
		if view[prefix] == prevTun {
			delete(view, prefix)
		}
	}
	if err := s.save(); err != nil {
		return err
	}
	return SavePCPinIPs(nil) // 旧 apply 清单迁移结束，运行态只留一个所有者。
}

// release 保留尚未过期的域名租约供重启恢复，但撤销全部本功能路由。
func (s *pcProxySession) release(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	empty := &dnsHookRules{proxied: map[string]bool{}}
	domains := s.Domains
	s.Domains = map[string]map[string]time.Time{}
	err := s.syncLocked(empty, "", now)
	s.Domains = domains
	if saveErr := s.save(); err == nil {
		err = saveErr
	}
	return err
}

func liveHookTunDevice() string {
	tun := hookTunDevice()
	if tun == "" {
		return ""
	}
	if _, err := hookInterface(tun); err != nil {
		return ""
	}
	return tun
}

// Windows 的路由接口 token 是 ifIndex，Unix 是接口名。
func hookInterface(token string) (*net.Interface, error) {
	if idx, err := strconv.Atoi(token); err == nil {
		return net.InterfaceByIndex(idx)
	}
	return net.InterfaceByName(token)
}
