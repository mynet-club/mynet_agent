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
	"sync"
	"time"

	"github.com/mynet-club/mynet_agent/shared"
)

type pcProxySession struct {
	mu      sync.Mutex
	Domains map[string]map[string]time.Time `json:"domains"`
	Routes  map[string]string               `json:"routes"` // prefix → tunnel interface
	// IPs 保留读面兼容；值是租约截止时间，不是 first-seen。
	IPs       map[string]time.Time `json:"ips"`
	LastError string               `json:"last_error,omitempty"`
	route     func(action, prefix, tun string) error
	inspect   func() (pcRouteView, error)
	save      func() error
}

func newPCProxySession() *pcProxySession {
	s := &pcProxySession{Domains: map[string]map[string]time.Time{}, Routes: map[string]string{}, IPs: map[string]time.Time{}}
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

func (s *pcProxySession) activeLocked(rules *dnsHookRules, now time.Time) map[string]time.Time {
	active := map[string]time.Time{}
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
			if active[ip].Before(until) {
				active[ip] = until
			}
		}
		if len(ips) == 0 {
			delete(s.Domains, name)
		}
	}
	for _, ip := range rules.customIPs {
		if v4 := net.ParseIP(ip).To4(); v4 != nil {
			active[v4.String()] = time.Time{} // 显式 IP 随意图启停，不伪造每次变动的 DNS 到期时间
		}
	}
	return active
}

// observe 不因缓存命中延长租约；until 必须来自 DNS 应答的剩余 TTL。
// 容量满时显式失败，不能驱逐仍被 OS 缓存使用的地址后让它意外直连。
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
	active := s.activeLocked(rules, now)
	if len(s.Domains) >= pcPinMaxIPs && s.Domains[name] == nil {
		return fmt.Errorf("active domain limit %d reached", pcPinMaxIPs)
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
		active[ip] = until
		if next[ip].Before(until) {
			next[ip] = until
		}
	}
	if len(active) > pcPinMaxIPs {
		return fmt.Errorf("active IP limit %d reached", pcPinMaxIPs)
	}
	if len(next) > 0 {
		s.Domains[name] = next
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
	active := s.activeLocked(rules, now)
	if len(active) > pcPinMaxIPs {
		return fmt.Errorf("active IP limit %d reached", pcPinMaxIPs)
	}
	s.IPs = active
	var ips []string
	for ip := range active {
		ips = append(ips, ip)
	}
	// 无待装/待撤路由时无需读取系统表（正常关闭、未访问任何域名）。
	if len(active) == 0 && len(s.Routes) == 0 {
		return nil
	}
	view, err := s.inspect()
	if err != nil {
		return err
	}
	want := map[string]bool{}
	if tun != "" {
		for _, prefix := range MergeIPsToCIDR(ips) {
			want[prefix] = true
		}
	}
	// 先持久化新增租约；失败时不改系统路由。
	if err := s.save(); err != nil {
		return err
	}
	// 删除只针对所有权日志里的前缀，不清系统表，也不删除他人接管的路由。
	var old []string
	for prefix := range s.Routes {
		old = append(old, prefix)
	}
	sort.Strings(old)
	for _, prefix := range old {
		prevTun := s.Routes[prefix]
		if want[prefix] && prevTun == tun {
			continue
		}
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
	var prefixes []string
	for prefix := range want {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	for _, prefix := range prefixes {
		// 内核已走隧道（可能被更宽的外部路由覆盖）就不接管其所有权。
		if view.covers(prefix, tun) {
			continue
		}
		if dev, exists := view[prefix]; exists && dev != tun {
			return fmt.Errorf("route %s belongs to %s", prefix, dev)
		}
		s.Routes[prefix] = tun
		// 写前记所有权，进程突然退出后仍可精确清理。
		if err := s.save(); err != nil {
			return err
		}
		if err := s.route("add", prefix, tun); err != nil {
			return fmt.Errorf("pin %s: %w", prefix, err)
		}
		view[prefix] = tun
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
