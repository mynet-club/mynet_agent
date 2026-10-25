package mynet

// 平台 DNS 接管（D4）—— 客户端 Win/Mac 把系统 DNS 指到**租户智能 DNS**。
//
//	启动：备份系统 DNS → 系统 DNS = dns.mynet.club / <tenant>.dns.mynet.club
//	停止：还原备份（幂等）
//
// PC 加速由 dns-hook 提供本地 forwarder；只有监听就绪后才接管到 127.0.0.1。
//
// crash 保护：备份写入 $MYNET_HOME/var/dns-backup.json；
// 下次启动发现「上次没还原」先补还原再重来。
//
// 网关/Linux 走 nft 劫持（DNSHijackCmds），不改系统 DNS —— 见 proxy_dns_hijack.go。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// DNSBackup 系统 DNS 备份（crash 后可还原）。
type DNSBackup struct {
	Platform   string              `json:"platform"`
	Interface  string              `json:"interface,omitempty"` // macOS 服务名 / Windows ifIndex
	Servers    []string            `json:"servers"`             // 原 DNS；空 = 跟随 DHCP
	Applied    bool                `json:"applied"`             // true = 已改、待还原
	AppliedAt  string              `json:"applied_at,omitempty"`
	RestoredAt string              `json:"restored_at,omitempty"`
	Services   map[string][]string `json:"services,omitempty"` // 每个接口原有设置，空列表 = DHCP
	// Target 接管时指向的 DNS（诊断用）
	Target string            `json:"target,omitempty"`
	Extra  map[string]string `json:"extra,omitempty"`
}

// dnsBackupPath 备份落盘位置（运行态，不进备份包）。
func dnsBackupPath() string {
	return filepath.Join(utils.VarDir(), "dns-backup.json")
}

// LoadDNSBackup 读备份；无文件返回 nil。
func LoadDNSBackup() *DNSBackup {
	b, err := os.ReadFile(dnsBackupPath())
	if err != nil {
		return nil
	}
	var d DNSBackup
	if json.Unmarshal(b, &d) != nil {
		return nil
	}
	return &d
}

func saveDNSBackup(d *DNSBackup) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dnsBackupPath()), 0755); err != nil {
		return err
	}
	p := dnsBackupPath()
	if err := os.WriteFile(p+".tmp", b, 0600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// ApplySystemDNS 改系统 DNS（单上游便捷入口）。
// 多上游（peer 优先 + smart 兜底）用 ApplySystemDNSMulti。
func ApplySystemDNS(iface, nameserver string) error {
	return ApplySystemDNSMulti(iface, []string{nameserver})
}

// ApplySystemDNSMulti 改系统 DNS 到**有序 nameserver 列表**（幂等）。
//
// 三选一（spec §13.1）落地：
//
//	peer        → [peer VIP...]
//	smart       → [smart IP...]
//	peer_smart  → [peer VIP..., smart IP...]（顺序即优先级，OS 自带 fallback）
//
// ⚠️ **指 peer 隧道 VIP 而非公网 DNS** —— 后者 UDP:53 可被链路劫持/污染。
// 本地 hook 就绪时允许 127.0.0.1，其余模式仍可直接指向上游。
//
// nameservers 为空 → 自动取租户 DNS（profile.TenantDNS > 约定域名 > 默认）。
// iface：macOS 网络服务名 / Windows ifIndex；空则自动探测。
//
// ⚠️ DNS 保险丝（干死过 DNS 的教训）：
//
//	① 写前探活：第一个上游必须能解析，否则拒绝（不把系统 DNS 指到死路）
//	② 写后验证：系统解析必须仍可用，失败立即回滚
//	③ 紧急恢复：mynet dns restore（只读备份，不依赖网络）
type systemDNSOps struct {
	list   func() ([]string, error)
	read   func(string) ([]string, string, error)
	write  func(string, []string) error
	probe  func(string) error
	verify func() error
}

func nativeSystemDNSOps() systemDNSOps {
	list := listNetworkServices
	if !isPCDNSPlatform() {
		list = func() ([]string, error) { return []string{""}, nil }
	}
	return systemDNSOps{list, readSystemDNS, writeSystemDNS, probeDNSAlive, verifySystemDNSWorks}
}

func ApplySystemDNSMulti(iface string, nameservers []string) error {
	if !isPCDNSPlatform() {
		return fmt.Errorf("system DNS takeover requires pc runtime mode")
	}
	if usesResolverFile(runtimeOS()) {
		if len(nameservers) == 0 {
			nameservers = []string{resolveClientDNS()}
		}
		var ips []string
		for _, ns := range nameservers {
			if isForbiddenDNS(ns) {
				return fmt.Errorf("DNS forwarder is not ready")
			}
			ip := resolveDNSHostToIP(ns)
			if ip == "" {
				return fmt.Errorf("cannot resolve DNS upstream %q", ns)
			}
			ips = append(ips, ip)
		}
		return applyResolverFileDNS("/etc/resolv.conf", ips, probeDNSAlive, verifySystemDNSWorks)
	}
	return applySystemDNSMulti(iface, nameservers, nativeSystemDNSOps())
}

func applySystemDNSMulti(iface string, nameservers []string, ops systemDNSOps) error {
	if prev := LoadDNSBackup(); prev != nil && prev.Applied {
		if err := restoreSystemDNS(ops); err != nil {
			return err
		}
	}
	if len(nameservers) == 0 || (len(nameservers) == 1 && nameservers[0] == "") {
		nameservers = []string{resolveClientDNS()}
	}
	var targets []string
	for _, ns := range nameservers {
		if isForbiddenDNS(ns) {
			return fmt.Errorf("refusing DNS %q without a live forwarder", ns)
		}
		ip := resolveDNSHostToIP(ns)
		if ip == "" {
			return fmt.Errorf("cannot resolve DNS upstream %q", ns)
		}
		targets = append(targets, ip)
	}
	if err := ops.probe(targets[0]); err != nil {
		return fmt.Errorf("DNS upstream probe: %w", err)
	}
	services := []string{iface}
	if iface == "" {
		var err error
		services, err = ops.list()
		if err != nil {
			return err
		}
	}
	backup := &DNSBackup{Platform: runtimeOS(), Applied: true, Target: strings.Join(targets, ","), AppliedAt: time.Now().UTC().Format(time.RFC3339), Services: map[string][]string{}}
	for _, service := range services {
		original, actual, err := ops.read(service)
		if err != nil {
			return err
		}
		backup.Services[actual] = original
	}
	if len(backup.Services) == 0 {
		return fmt.Errorf("no network service available")
	}
	// 先写备份再接管。中途失败/进程退出也能按接口恢复，绝不把一张网卡的 DNS 灌到所有网卡。
	if err := saveDNSBackup(backup); err != nil {
		return err
	}
	for service := range backup.Services {
		if err := ops.write(service, targets); err != nil {
			return errors.Join(err, restoreSystemDNS(ops))
		}
	}
	if err := ops.verify(); err != nil {
		return errors.Join(err, restoreSystemDNS(ops))
	}
	return nil
}

// ApplyProxyDNSPlan 按三选一模式落地系统 DNS（spec §13.1）。
// mode=off/空 → 不碰；否则按 Nameservers() 顺序写。
func ApplyProxyDNSPlan(iface string, d *domain.ProxyDNS) error {
	if !isPCDNSPlatform() || !d.Active() {
		return nil // ③ 不加速（默认）—— 不碰 DNS
	}
	return ApplySystemDNSMulti(iface, d.Nameservers())
}

// resolveDNSHostToIP 域名→IPv4。已是 IP 则原样返回。
// networksetup / 某些 Linux 工具只认 IP；写入前必须解析（跨平台）。
func resolveDNSHostToIP(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if net.ParseIP(host) != nil {
		return host
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return ""
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

// isForbiddenDNS 禁止写入的 DNS（干死过网络的错误值）。
// 192.168.100.x 是 peer/隧道 NAT 网段，不是本机 DNS（本机=192.168.0.1/DHCP）。
// 127.0.0.1 默认禁（无 forwarder 就是黑洞）；dns-hook 存活时放行。
func isForbiddenDNS(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "192.168.100.") {
		return true
	}
	if s == "127.0.0.1" {
		// 钩子 forwarder 在跑 → 127.0.0.1:53 有应答，合法
		if pid := readHookPID(); pid > 0 && processAlive(pid) {
			return false
		}
		return true
	}
	return false
}

// writeSystemDNSAll 对所有启用网卡写 DNS（或 DHCP）。
// 实测教训：只写一张网卡，其它仍卡在错 DNS —— 必须全量。
func writeSystemDNSAll(servers []string) error {
	for _, s := range servers {
		if isForbiddenDNS(s) {
			return fmt.Errorf("拒绝写入禁止 DNS %q（192.168.100.x 是 peer 网段，127.0.0.1 无 forwarder）", s)
		}
		// 127.0.0.1 + 钩子存活：探活走本机 :53
	}
	ifaces, err := listNetworkServices()
	if err != nil {
		return writeSystemDNS("", servers)
	}
	var lastErr error
	ok := 0
	for _, iface := range ifaces {
		if err := writeSystemDNS(iface, servers); err != nil {
			lastErr = err
			continue
		}
		ok++
	}
	if ok == 0 && lastErr != nil {
		return lastErr
	}
	return nil
}

// probeDNSAlive 直接 UDP 探目标 DNS：必须能答一个 A 查询（保险丝①）。
func probeDNSAlive(serverIP string) error {
	res := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, "udp", net.JoinHostPort(serverIP, "53"))
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, err := res.LookupHost(ctx, "baidu.com")
	if err != nil {
		_, err2 := res.LookupHost(ctx, "qq.com")
		if err2 != nil {
			return fmt.Errorf("baidu/qq 均解析失败: %v / %v", err, err2)
		}
	}
	return nil
}

// verifySystemDNSWorks 写后验证：走系统解析器必须还能解析（保险丝②）。
func verifySystemDNSWorks() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := net.DefaultResolver.LookupHost(ctx, "qq.com")
	if err != nil {
		_, err2 := net.DefaultResolver.LookupHost(ctx, "baidu.com")
		if err2 != nil {
			return fmt.Errorf("系统解析器失效: %v / %v", err, err2)
		}
	}
	return nil
}

// resolveClientDNS 客户端接管目标：租户智能 DNS（D1/D4）。
// 优先级：profile.TenantDNS > <tenant>.dns.mynet.club > dns.mynet.club。
func resolveClientDNS() string {
	// 从 proxy 策略拿 PlatformDNS（可能已被 ApplyTenantDNS 填过）
	region := ""
	if p := EffectiveProxyPolicy(); p != nil {
		region = p.Region
		if p.DomainRules != nil {
			if ns := p.DomainRules.EffectivePlatformDNS(); ns != "" && ns != domain.PlatformDNSDefault {
				return ns // 显式配置优先
			}
		}
	}
	// 从 online profile 拿租户 DNS（services 表覆盖）
	if ns := tenantDNSFromProfile(); ns != "" {
		return ns
	}
	// 平台默认：大陆 → dns.dsplat.com；非大陆 → dns.mynet.club
	return domain.PlatformDNSForRegion(region)
}

// tenantDNSFromProfile 从 mode.json + profile 读租户智能 DNS（D1）。
// 无 profile 时返回空。
func tenantDNSFromProfile() string {
	store := &config.Store{}
	_, mode, err := store.ActiveSource()
	if err != nil || mode == nil || mode.Active.TenantID == "" {
		return ""
	}
	p, err := store.LoadOnlineProfile(mode.Active.TenantID)
	if err != nil || p == nil {
		return ""
	}
	return p.EffectiveTenantDNS()
}

// RestoreSystemDNS 还原系统 DNS（幂等）。
func RestoreSystemDNS() error { return restoreSystemDNS(nativeSystemDNSOps()) }

func restoreSystemDNS(ops systemDNSOps) error {
	backup := LoadDNSBackup()
	if backup == nil || !backup.Applied {
		return nil
	}
	if backup.Extra["resolver_file"] != "" {
		return restoreResolverFileDNS(backup)
	}
	originals := backup.Services
	if len(originals) == 0 {
		originals = map[string][]string{backup.Interface: backup.Servers}
	}
	var errs []error
	for service, original := range originals {
		current, _, err := ops.read(service)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		// 用户/其它软件已经修改的接口不再归本次接管，保留其设置。
		if backup.Target != "" && !reflect.DeepEqual(current, strings.Split(backup.Target, ",")) {
			continue
		}
		if err := ops.write(service, original); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	backup.Applied = false
	backup.RestoredAt = time.Now().UTC().Format(time.RFC3339)
	return saveDNSBackup(backup)
}

// 仅 hook 自己接管到 loopback 的 DNS 才能由 hook 退出路径恢复。
func restoreHookDNS() error {
	b := LoadDNSBackup()
	if b == nil || !b.Applied || b.Target != "127.0.0.1" {
		return nil
	}
	return RestoreSystemDNS()
}

// DNSRestoreNeeded 是否有未还原的接管（诊断用）。
func DNSRestoreNeeded() bool {
	b := LoadDNSBackup()
	return b != nil && b.Applied
}

// isPCDNSPlatform 客户端形态：改系统 DNS 到本机 53。
// Mac/Win 没有 nft OUTPUT 劫持，走「改系统 DNS」；Linux 网关走 nft 劫持。
func isPCDNSPlatform() bool { return pcRuntimeForOS(runtimeOS()) }
