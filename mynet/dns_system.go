package mynet

// 平台 DNS 接管（D4）—— 客户端 Win/Mac 把系统 DNS 指到**租户智能 DNS**。
//
//	启动：备份系统 DNS → 系统 DNS = dns.mynet.club / <tenant>.dns.mynet.club
//	停止：还原备份（幂等）
//
// ⚠️ 客户端不自建 DNS、**无本地 forwarder**（已裁掉，徒增体积）：
// 系统 DNS 直接指平台智能 DNS，域名分流在服务端做。
// 不要写 127.0.0.1 —— 那会把解析指到空端口（实测教训）。
//
// crash 保护：备份写入 $MYNET_HOME/var/dns-backup.json；
// 下次启动发现「上次没还原」先补还原再重来。
//
// 网关/Linux 走 nft 劫持（DNSHijackCmds），不改系统 DNS —— 见 proxy_dns_hijack.go。

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// DNSBackup 系统 DNS 备份（crash 后可还原）。
type DNSBackup struct {
	Platform   string   `json:"platform"`
	Interface  string   `json:"interface,omitempty"` // macOS 服务名 / Windows ifIndex
	Servers    []string `json:"servers"`             // 原 DNS；空 = 跟随 DHCP
	Applied    bool     `json:"applied"`             // true = 已改、待还原
	AppliedAt  string   `json:"applied_at,omitempty"`
	RestoredAt string   `json:"restored_at,omitempty"`
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
	return os.WriteFile(dnsBackupPath(), b, 0o600)
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
// ⚠️ 客户端（Mac/Win）不自建 DNS、无本地 forwarder：系统 DNS 直接指上游。
// 不要写 127.0.0.1 —— 那会把解析指到空端口（实测教训）。
//
// nameservers 为空 → 自动取租户 DNS（profile.TenantDNS > 约定域名 > 默认）。
// iface：macOS 网络服务名 / Windows ifIndex；空则自动探测。
//
// ⚠️ DNS 保险丝（干死过 DNS 的教训）：
//
//	① 写前探活：第一个上游必须能解析，否则拒绝（不把系统 DNS 指到死路）
//	② 写后验证：系统解析必须仍可用，失败立即回滚
//	③ 紧急恢复：mynet dns restore（只读备份，不依赖网络）
func ApplySystemDNSMulti(iface string, nameservers []string) error {
	// crash 保护：上次改了没还原 → 先还原（避免叠加在错误状态上）
	if prev := LoadDNSBackup(); prev != nil && prev.Applied {
		if err := RestoreSystemDNS(); err != nil {
			return fmt.Errorf("上次 DNS 未还原且还原失败: %w", err)
		}
	}

	// 目标 DNS 列表：显式 > 租户智能 DNS
	if len(nameservers) == 0 || (len(nameservers) == 1 && nameservers[0] == "") {
		nameservers = []string{resolveClientDNS()}
	}
	// 域名 → IP（networksetup 只认 IP）；过滤禁写值
	targetIPs := make([]string, 0, len(nameservers))
	for _, ns := range nameservers {
		if isForbiddenDNS(ns) {
			return fmt.Errorf("拒绝写入禁止 DNS %q（192.168.100.x 是 peer 网段，127.0.0.1 无监听）", ns)
		}
		ip := resolveDNSHostToIP(ns)
		if ip == "" {
			// 非首个上游解析不出 → 跳过（兜底项可缺）；首个必须成功
			if len(targetIPs) == 0 {
				return fmt.Errorf("DNS 目标 %q 解析不出 IP，拒绝接管（保险丝①）", ns)
			}
			continue
		}
		targetIPs = append(targetIPs, ip)
	}
	if len(targetIPs) == 0 {
		return fmt.Errorf("无可用 DNS 上游，拒绝接管")
	}

	cur, usedIface, err := readSystemDNS(iface)
	if err != nil {
		return fmt.Errorf("读系统 DNS 失败: %w", err)
	}

	// 保险丝①：写前探活 —— 第一个上游（primary）必须能解析
	if err := probeDNSAlive(targetIPs[0]); err != nil {
		return fmt.Errorf("DNS 目标 %s 探活失败，拒绝接管（保险丝①）: %w", targetIPs[0], err)
	}

	if err := writeSystemDNSAll(targetIPs); err != nil {
		return fmt.Errorf("写系统 DNS 失败: %w", err)
	}

	// 保险丝②：写后验证 —— 系统解析必须仍可用，失败立即回滚
	if err := verifySystemDNSWorks(); err != nil {
		_ = writeSystemDNSAll(cur) // 回滚
		return fmt.Errorf("接管后系统解析失败，已回滚（保险丝②）: %w", err)
	}

	backup := &DNSBackup{
		Platform:  runtimeOS(),
		Interface: usedIface,
		Servers:   cur,
		Applied:   true,
		Target:    strings.Join(targetIPs, ","),
		AppliedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := saveDNSBackup(backup); err != nil {
		// 备份写失败要立刻回滚，否则 crash 后还原不回来
		_ = writeSystemDNSAll(cur)
		return fmt.Errorf("写 DNS 备份失败，已回滚: %w", err)
	}
	return nil
}

// ApplyProxyDNSPlan 按三选一模式落地系统 DNS（spec §13.1）。
// mode=off/空 → 不碰；否则按 Nameservers() 顺序写。
func ApplyProxyDNSPlan(iface string, d *domain.ProxyDNS) error {
	if !d.Active() {
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
func isForbiddenDNS(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "192.168.100.") {
		return true
	}
	if s == "127.0.0.1" {
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
func RestoreSystemDNS() error {
	backup := LoadDNSBackup()
	// 无备份 = 没接管过，但用户要「恢复」时**安全默认是 DHCP**（不卡在错值上）
	if backup == nil {
		return writeSystemDNS("", nil) // 跟随 DHCP
	}
	if !backup.Applied {
		return nil // 已还原过
	}

	// 空列表 = 原本跟随 DHCP → 明确写回 DHCP（各平台 reset）
	// ⚠️ 绝不能把「空」当成无操作 —— 上次接管前是 DHCP，就必须回 DHCP
	servers := backup.Servers
	if len(servers) == 0 {
		servers = nil // 显式 DHCP
	}
	if err := writeSystemDNSAll(servers); err != nil {
		return fmt.Errorf("还原系统 DNS 失败: %w", err)
	}
	backup.Applied = false
	backup.RestoredAt = time.Now().UTC().Format(time.RFC3339)
	return saveDNSBackup(backup)
}

// DNSRestoreNeeded 是否有未还原的接管（诊断用）。
func DNSRestoreNeeded() bool {
	b := LoadDNSBackup()
	return b != nil && b.Applied
}

// isPCDNSPlatform 客户端形态：改系统 DNS 到本机 53。
// Mac/Win 没有 nft OUTPUT 劫持，走「改系统 DNS」；Linux 网关走 nft 劫持。
func isPCDNSPlatform() bool {
	switch runtimeOS() {
	case "darwin", "windows":
		return true
	default:
		return false
	}
}
