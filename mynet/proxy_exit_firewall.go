package mynet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

// 出口规则按接口归属；所有系统写入仍由 executeApply 提供的 runner 执行。
type proxyFirewallRun func(argv []string, input string) (string, error)

func proxyExitName(dev string) string {
	sum := sha256.Sum256([]byte(dev))
	return "mynet_exit_" + hex.EncodeToString(sum[:6])
}

func proxyExitSources(p *domain.ProxyPolicy) ([]string, bool, error) {
	if p == nil || !p.Enabled || !p.IsServerLike() {
		return nil, false, nil
	}
	acl := p.ACL
	if acl != nil && len(acl.DeniedNodeIDs) > 0 {
		return nil, false, fmt.Errorf("出口 ACL 缺少可执行拒绝地址，拒绝放开")
	}
	if acl == nil || acl.EffectiveMode() == domain.ProxyACLOpen {
		return nil, false, nil
	}
	// denylist 只有节点 id，无可执行的拒绝地址；不能冒充已落实包过滤。
	if acl.EffectiveMode() != domain.ProxyACLAllowlist || len(acl.DeniedNodeIDs) > 0 {
		return nil, false, fmt.Errorf("出口 ACL 缺少可执行拒绝地址，拒绝放开")
	}
	seen := map[string]bool{}
	for _, s := range acl.AllowedCIDRs {
		_, network, err := net.ParseCIDR(s)
		if err != nil {
			return nil, false, fmt.Errorf("出口 ACL CIDR 无效: %q", s)
		}
		seen[network.String()] = true
	}
	cidrs := make([]string, 0, len(seen))
	for s := range seen {
		cidrs = append(cidrs, s)
	}
	sort.Strings(cidrs)
	return cidrs, true, nil // 空白名单 CIDR 也必须 DROP，不能开放。
}

func isProxyServerRole(role string) bool {
	return role == domain.ProxyRoleServer || role == domain.ProxyRoleBoth
}

// proxyExitExplicitOff 判断当前是否属于「显式关闭或撤销」状态。
// 默认策略为开放（不自动注入 DROP 链）；只有历史云端授权被撤销（legacy=true）
// 或存在显式停用的 server/both 策略时才下发 DROP 规则。
func proxyExitExplicitOff(p *domain.ProxyPolicy, legacy bool) bool {
	if p != nil && p.Enabled && p.IsServerLike() {
		return false
	}
	if legacy {
		return true
	}
	return p != nil && !p.Enabled && isProxyServerRole(p.Role)
}

// 单一 nft batch 原子替换整张专表；失败时内核保留上一代。
func proxyExitNftBatch(dev string, scope proxyExitScope, p *domain.ProxyPolicy, exists bool) (string, error) {
	return proxyExitNftBatchWithDeny(dev, scope, p, exists, false)
}

func proxyExitNftBatchWithDeny(dev string, scope proxyExitScope, p *domain.ProxyPolicy, exists, explicitOff bool) (string, error) {
	if err := domain.ValidateTunDevice(dev); err != nil {
		return "", err
	}
	name := proxyExitName(dev)
	enabled := p != nil && p.Enabled && p.IsServerLike()
	if !enabled && !explicitOff {
		// 默认开放：不新建 DROP 专表；若存在历史残留专表则删除。
		if exists {
			return fmt.Sprintf("delete table inet %s\n", name), nil
		}
		return "", nil
	}
	cidrs, restricted, err := proxyExitSources(p)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if exists {
		fmt.Fprintf(&b, "delete table inet %s\n", name)
	}
	if err := scope.validate(dev); err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "add table inet %s\nadd chain inet %s forward { type filter hook forward priority -10; policy accept; }\n", name, name)
	wan := scope.nftWAN()
	match := fmt.Sprintf("iifname %q oifname %s", dev, wan)
	for _, cidr := range scope.LAN {
		family := "ip"
		if ip, _, _ := net.ParseCIDR(cidr); ip.To4() == nil {
			family = "ip6"
		}
		fmt.Fprintf(&b, "add rule inet %s forward %s %s daddr %s return\n", name, match, family, cidr)
	}
	if enabled {
		if restricted {
			for _, cidr := range cidrs {
				ip, _, _ := net.ParseCIDR(cidr)
				family := "ip"
				if ip.To4() == nil {
					family = "ip6"
				}
				fmt.Fprintf(&b, "add rule inet %s forward %s %s saddr %s accept\n", name, match, family, cidr)
			}
			fmt.Fprintf(&b, "add rule inet %s forward %s drop\n", name, match)
		} else {
			fmt.Fprintf(&b, "add rule inet %s forward %s accept\n", name, match)
		}
		fmt.Fprintf(&b, "add rule inet %s forward iifname %s oifname %q ct state established,related accept\n", name, wan, dev)
		fmt.Fprintf(&b, "add chain inet %s postrouting { type nat hook postrouting priority 100; }\nadd rule inet %s postrouting %s masquerade\n", name, name, match)
	} else {
		// 显式关闭或撤销：保留 WAN 拒绝，不允许平台 forwarding/NAT 绕过。
		fmt.Fprintf(&b, "add rule inet %s forward %s drop\n", name, match)
	}
	return b.String(), nil
}

func proxyFirewallMissing(out string, err error) bool {
	if err == nil {
		return false
	}
	// 权限不足、工具缺失、解析失败都必须报错，不能当作规则不存在。
	return strings.Contains(out, "No such file or directory") || strings.Contains(out, "does not exist")
}

func syncProxyExitFirewall(snap *shared.SystemSnapshot, p *domain.ProxyPolicy, dev string, legacy bool, run proxyFirewallRun) error {
	enabled := p != nil && p.Enabled && p.IsServerLike()
	explicitOff := proxyExitExplicitOff(p, legacy)
	if snap == nil || snap.OS == "darwin" || snap.OS == "windows" || snap.OS == "freebsd" || snap.OS == "openbsd" || snap.OS == "netbsd" {
		if legacy || enabled {
			return fmt.Errorf("当前平台不支持 Linux 出口规则管理")
		}
		return nil
	}
	if err := domain.ValidateTunDevice(dev); err != nil {
		return err
	}
	var scope proxyExitScope
	if enabled || explicitOff {
		var err error
		scope, err = proxyExitScopeFromSnapshot(snap, dev)
		if err != nil {
			return err
		}
	}
	if snap.Capabilities["nft"] {
		name := proxyExitName(dev)
		out, err := run([]string{"nft", "list", "table", "inet", name}, "")
		exists := err == nil
		if err != nil && !proxyFirewallMissing(out, err) {
			return fmt.Errorf("读取出口 nft 规则: %w", err)
		}
		batch, err := proxyExitNftBatchWithDeny(dev, scope, p, exists, explicitOff)
		if err != nil {
			return err
		}
		// 历史 handle 删除与专表替换在同一 batch；任何一步失败都保留上一代。
		if legacy {
			deletes, err := legacyNftExitDeletes(dev, run)
			if err != nil {
				return err
			}
			batch += deletes
		}
		if batch != "" {
			if _, err = run([]string{"nft", "-f", "-"}, batch); err != nil {
				return fmt.Errorf("原子替换/撤销出口 nft: %w", err)
			}
		}
		return nil
	}
	if snap.Capabilities["iptables"] {
		return syncProxyExitIPTablesWithDeny(p, dev, scope, legacy, explicitOff, run)
	}
	if legacy || enabled {
		return fmt.Errorf("出口无可用 nft/iptables，拒绝假成功")
	}
	return nil
}

// 迁移仅认旧生成器的完整规则形状，且调用方必须有已管理出口的 Cloud 记录。
// 注释、其他匹配条件、其他接口/链一律保留；不 flush 共享表或链。
func legacyNftExitDeletes(dev string, run proxyFirewallRun) (string, error) {
	var batch strings.Builder
	specs := [][2]string{{"nat", "POSTROUTING"}, {"filter", "FORWARD"}}
	prefix := `^iifname "` + regexp.QuoteMeta(dev) + `" `
	nat := regexp.MustCompile(prefix + `masquerade # handle ([0-9]+)$`)
	acl := regexp.MustCompile(prefix + `(?:ip saddr ([0-9./]+) accept|drop) # handle ([0-9]+)$`)
	for _, spec := range specs {
		out, err := run([]string{"nft", "-a", "list", "chain", "ip", spec[0], spec[1]}, "")
		if err != nil {
			if proxyFirewallMissing(out, err) {
				continue
			}
			return "", fmt.Errorf("读取旧出口规则: %w", err)
		}
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			handle := ""
			if spec[0] == "nat" {
				if m := nat.FindStringSubmatch(line); m != nil {
					handle = m[1]
				}
			} else if m := acl.FindStringSubmatch(line); m != nil {
				if m[1] != "" {
					if _, _, err := net.ParseCIDR(m[1]); err != nil && net.ParseIP(m[1]) == nil {
						continue
					}
				}
				handle = m[2]
			}
			if handle != "" {
				fmt.Fprintf(&batch, "delete rule ip %s %s handle %s\n", spec[0], spec[1], handle)
			}
		}
	}
	return batch.String(), nil
}

// iptables 的出口使用专用 filter/mangle/nat 链。POSTROUTING 不支持 -i，
// 所以在 FORWARD 标记入隧道的包，再在 NAT 按标记匹配。保留 mark 低 8 位；
// 0x4dxxxx00/0xffffff00 是 MyNet 出口保留段，与 client fwmark 0xc8 分开。
func proxyExitIPTDesired(dev string, scope proxyExitScope, p *domain.ProxyPolicy) (map[string][]string, error) {
	return proxyExitIPTDesiredWithDeny(dev, scope, p, false)
}

func proxyExitIPTDesiredWithDeny(dev string, scope proxyExitScope, p *domain.ProxyPolicy, explicitOff bool) (map[string][]string, error) {
	if err := domain.ValidateTunDevice(dev); err != nil {
		return nil, err
	}
	out := map[string][]string{"filter": nil, "mangle": nil, "nat": nil}
	enabled := p != nil && p.Enabled && p.IsServerLike()
	if !enabled && !explicitOff {
		// 默认开放：返回空期望，不新建 DROP 链，并清理任何历史残留专链。
		return out, nil
	}
	cidrs, restricted, err := proxyExitSources(p)
	if err != nil {
		return nil, err
	}
	if err := scope.validate(dev); err != nil {
		return nil, err
	}
	name := proxyExitName(dev)
	sum := sha256.Sum256([]byte(dev))
	mark := fmt.Sprintf("0x4d%02x%02x00/0xffffff00", sum[0], sum[1])
	for _, cidr := range cidrs {
		ip, _, _ := net.ParseCIDR(cidr)
		if ip.To4() == nil {
			return nil, fmt.Errorf("iptables 出口 IPv6 ACL 需要 ip6tables，拒绝忽略")
		}
	}
	f := []string{":" + name + " - [0:0]"}
	for _, cidr := range scope.LAN {
		if ip, _, _ := net.ParseCIDR(cidr); ip.To4() != nil {
			f = append(f, "-A "+name+" -d "+cidr+" -j RETURN")
		}
	}

	if enabled && restricted {
		for _, cidr := range cidrs {
			f = append(f, "-A "+name+" -s "+cidr+" -j ACCEPT")
		}
		f = append(f, "-A "+name+" -j DROP")
	} else if enabled {
		f = append(f, "-A "+name+" -j ACCEPT")
	} else {
		f = append(f, "-A "+name+" -j DROP")
	}
	for _, wan := range scope.WAN {
		f = append(f, "-I FORWARD 1 -i "+dev+" -o "+wan+" -j "+name)
	}
	out["filter"] = f
	if !enabled {
		return out, nil
	}
	out["mangle"] = []string{":" + name + " - [0:0]", "-A " + name + " -j MARK --set-xmark " + mark}
	for _, wan := range scope.WAN {
		out["mangle"] = append(out["mangle"], "-I FORWARD 1 -i "+dev+" -o "+wan+" -j "+name)
	}
	out["nat"] = []string{":" + name + " - [0:0]", "-A " + name + " -j MASQUERADE", "-I POSTROUTING 1 -m mark --mark " + mark + " -j " + name}
	return out, nil
}

// 从 -S 快照只摘本出口链和指向它的跳转，恢复不覆盖其它系统规则。
func proxyExitIPTOwned(snapshot, name string) []string {
	var out []string
	for _, line := range strings.Split(snapshot, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "-N" && fields[1] == name {
			out = append(out, ":"+name+" - [0:0]")
			continue
		}
		if fields[0] != "-A" {
			continue
		}
		own := fields[1] == name
		for i := 2; i+1 < len(fields); i++ {
			if fields[i] == "-j" && fields[i+1] == name {
				own = true
			}
		}
		if own {
			out = append(out, line)
		}
	}
	return out
}
func proxyExitIPTRestore(table, snapshot, name string, desired []string) string {
	old := proxyExitIPTOwned(snapshot, name)
	var b strings.Builder
	fmt.Fprintf(&b, "*%s\n", table)
	// 先删本出口的所有跳转（包括历史重复），不碰其他链。
	hasChain := false
	for _, line := range old {
		if strings.HasPrefix(line, ":") {
			hasChain = true
			continue
		}
		if !strings.HasPrefix(line, "-A "+name+" ") {
			b.WriteString(strings.Replace(line, "-A ", "-D ", 1) + "\n")
		}
	}
	if len(desired) > 0 {
		for _, line := range desired {
			if strings.HasPrefix(line, ":") {
				b.WriteString(line + "\n")
			}
		}
		if hasChain {
			b.WriteString("-F " + name + "\n")
		}
		for _, line := range desired {
			if !strings.HasPrefix(line, ":") {
				b.WriteString(line + "\n")
			}
		}
	} else if hasChain {
		fmt.Fprintf(&b, "-F %s\n-X %s\n", name, name)
	}
	b.WriteString("COMMIT\n")
	return b.String()
}
func syncProxyExitIPTables(p *domain.ProxyPolicy, dev string, scope proxyExitScope, legacy bool, run proxyFirewallRun) error {
	return syncProxyExitIPTablesWithDeny(p, dev, scope, legacy, proxyExitExplicitOff(p, legacy), run)
}

func syncProxyExitIPTablesWithDeny(p *domain.ProxyPolicy, dev string, scope proxyExitScope, legacy, explicitOff bool, run proxyFirewallRun) error {
	desired, err := proxyExitIPTDesiredWithDeny(dev, scope, p, explicitOff)
	if err != nil {
		return err
	}
	name := proxyExitName(dev)
	tables := []string{"filter", "mangle", "nat"}
	snapshots := map[string]string{}
	for _, table := range tables {
		out, err := run([]string{"iptables", "-w", "5", "-t", table, "-S"}, "")
		if err != nil {
			return fmt.Errorf("读取 %s 出口规则: %w", table, err)
		}
		snapshots[table] = out
	}
	// 不能让其它接口/系统已有 MARK 与本出口 NAT 标记相撞。
	if err := proxyExitMarkConflict(snapshots["mangle"], name, desired["mangle"]); err != nil {
		return err
	}
	enabled := p != nil && p.Enabled && p.IsServerLike()
	if !enabled {
		// 先撤 NAT 再撤 ACL，不制造“仍可 NAT、却已无白名单”的窗口。
		tables = []string{"nat", "mangle", "filter"}
	}
	// filter 先提交（deny 先于 NAT）；每张表原子提交，失败只回退本出口。
	var touched []string
	for _, table := range tables {
		if !enabled && !explicitOff && !legacy && len(proxyExitIPTOwned(snapshots[table], name)) == 0 {
			continue
		}
		batch := proxyExitIPTRestore(table, snapshots[table], name, desired[table])
		if legacy {
			var deletes strings.Builder
			for _, line := range strings.Split(snapshots[table], "\n") {
				if legacyIPTExitRule(strings.Fields(line), dev, table) {
					deletes.WriteString(strings.Replace(line, "-A ", "-D ", 1) + "\n")
				}
			}
			batch = strings.Replace(batch, "*"+table+"\n", "*"+table+"\n"+deletes.String(), 1)
		}
		if _, err = run([]string{"iptables-restore", "--wait", "5", "--noflush"}, batch); err != nil {
			var failures []string
			for i := len(touched) - 1; i >= 0; i-- {
				t := touched[i]
				current, e := run([]string{"iptables", "-w", "5", "-t", t, "-S"}, "")
				if e == nil {
					prior := proxyExitIPTOwned(snapshots[t], name)
					for i, line := range prior {
						if strings.HasPrefix(line, "-A ") && !strings.HasPrefix(line, "-A "+name+" ") {
							fields := strings.Fields(line)
							prior[i] = "-I " + fields[1] + " 1 " + strings.Join(fields[2:], " ")
						}
					}
					restore := proxyExitIPTRestore(t, current, name, prior)
					if legacy {
						var restoreLegacy strings.Builder
						lines := strings.Split(snapshots[t], "\n")
						for i := len(lines) - 1; i >= 0; i-- {
							fields := strings.Fields(lines[i])
							if legacyIPTExitRule(fields, dev, t) {
								restoreLegacy.WriteString("-I " + fields[1] + " 1 " + strings.Join(fields[2:], " ") + "\n")
							}
						}
						restore = strings.Replace(restore, "COMMIT\n", restoreLegacy.String()+"COMMIT\n", 1)
					}
					_, e = run([]string{"iptables-restore", "--wait", "5", "--noflush"}, restore)
				}
				if e != nil {
					failures = append(failures, t+": "+e.Error())
				}
			}
			return fmt.Errorf("出口 %s 应用失败: %w; 回退错误=%v", table, err, failures)
		}
		touched = append(touched, table)
	}
	return nil
}
func legacyIPTExitRule(f []string, dev, table string) bool {
	if len(f) == 6 && f[0] == "-A" && f[2] == "-i" && f[3] == dev && f[4] == "-j" {
		return table == "filter" && f[1] == "FORWARD" && (f[5] == "ACCEPT" || f[5] == "DROP") || table == "nat" && f[1] == "POSTROUTING" && f[5] == "MASQUERADE"
	}
	if table == "filter" && len(f) == 6 && strings.Join(f, " ") == "-A FORWARD -o "+dev+" -j ACCEPT" {
		return true
	}
	if table == "filter" && len(f) == 8 && f[0] == "-A" && f[1] == "FORWARD" && f[2] == "-i" && f[3] == dev && f[4] == "-s" && f[6] == "-j" && f[7] == "ACCEPT" {
		_, _, err := net.ParseCIDR(f[5])
		return err == nil
	}
	return false
}

func proxyExitMarkConflict(snapshot, name string, desired []string) error {
	var wanted uint64
	for _, line := range desired {
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "--set-xmark" {
				wanted, _ = strconv.ParseUint(strings.Split(fields[i+1], "/")[0], 0, 32)
			}
		}
	}
	if wanted == 0 {
		return nil
	}
	for _, line := range strings.Split(snapshot, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] == name {
			continue
		}
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] != "--set-xmark" && fields[i] != "--set-mark" {
				continue
			}
			parts := strings.Split(fields[i+1], "/")
			value, err := strconv.ParseUint(parts[0], 0, 32)
			if err != nil {
				continue
			}
			mask := uint64(0xffffffff)
			if len(parts) == 2 {
				mask, err = strconv.ParseUint(parts[1], 0, 32)
				if err != nil {
					continue
				}
			}
			overlap := mask & 0xffffff00
			if overlap != 0 && value&overlap == wanted&overlap {
				return fmt.Errorf("出口 MARK 与 %s 链的既有标记冲突", fields[1])
			}
		}
	}
	return nil
}

// 重放产物不依赖内核现状：add + flush 专表，冷启动与重复加载同义。
func writeProxyExitReplayArtifact(home, dev string, scope proxyExitScope, p *domain.ProxyPolicy) error {
	return writeProxyExitReplayArtifactWithDeny(home, dev, scope, p, false)
}

func writeProxyExitReplayArtifactWithDeny(home, dev string, scope proxyExitScope, p *domain.ProxyPolicy, explicitOff bool) error {
	path := filepath.Join(home, "scripts", "firewall."+proxyExitName(dev)+".nft")
	enabled := p != nil && p.Enabled && p.IsServerLike()
	if !enabled && !explicitOff {
		// 默认开放：不生成 DROP 重放产物；若存在历史残留文件则清理。
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	batch, err := proxyExitNftBatchWithDeny(dev, scope, p, false, explicitOff)
	if err != nil {
		return err
	}
	prefix := "add table inet " + proxyExitName(dev) + "\n"
	batch = prefix + "flush table inet " + proxyExitName(dev) + "\n" + strings.TrimPrefix(batch, prefix)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return writeFirewallArtifact(path, []byte(batch), 0600)
}

// proxyExitExplicitOffFromState 从本地持久化 proxy_intent.json 判断出口是否被显式关闭。
func proxyExitExplicitOffFromState() bool {
	st, err := LoadProxyState()
	if err != nil || st == nil {
		return false
	}
	if eff := st.Effective(); eff != nil && eff.Policy.Enabled && eff.Policy.IsServerLike() {
		return false
	}
	if st.Cloud != nil && !st.Cloud.Policy.Enabled && isProxyServerRole(st.Cloud.Policy.Role) {
		return true
	}
	if st.Local != nil && !st.Local.Policy.Enabled && isProxyServerRole(st.Local.Policy.Role) {
		return true
	}
	return false
}

// ensureProxyExitReplay 在 sync 阶段同步重放产物：默认开放时不写 DROP 文件，
// 仅在出口已启用或显式关闭时生成对应专表重放脚本。
func ensureProxyExitReplay(home, dev string) error {
	if dev == "" {
		return nil
	}
	snap, err := shared.ReadSnapshot(shared.DefaultSnapshotPath())
	if err != nil {
		return fmt.Errorf("出口重放需要 detector 快照: %w", err)
	}
	if snap.OS != "linux" || !snap.Capabilities["nft"] {
		return nil
	}
	p := EffectiveProxyPolicy()
	explicitOff := proxyExitExplicitOffFromState()
	var scope proxyExitScope
	if (p != nil && p.Enabled && p.IsServerLike()) || explicitOff {
		var scopeErr error
		scope, scopeErr = proxyExitScopeFromSnapshot(snap, dev)
		if scopeErr != nil {
			return scopeErr
		}
	}
	return writeProxyExitReplayArtifactWithDeny(home, dev, scope, p, explicitOff)
}

// WAN is every detected default egress. Directly attached networks remain local
// even on a one-NIC bypass router where LAN and WAN share the same interface.
type proxyExitScope struct{ WAN, LAN []string }

func (s proxyExitScope) validate(dev string) error {
	if len(s.WAN) == 0 {
		return fmt.Errorf("出口防火墙需要 detector 的物理 WAN 路由快照")
	}
	for _, wan := range s.WAN {
		if err := domain.ValidateTunDevice(wan); err != nil {
			return err
		}
		if wan == dev {
			return fmt.Errorf("WAN cannot be the tunnel")
		}
	}
	for _, cidr := range s.LAN {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return err
		}
		bits, _ := n.Mask.Size()
		if bits == 0 {
			return fmt.Errorf("LAN exception cannot cover default route")
		}
	}
	return nil
}
func (s proxyExitScope) nftWAN() string {
	parts := make([]string, 0, len(s.WAN))
	for _, wan := range s.WAN {
		parts = append(parts, strconv.Quote(wan))
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}
func proxyExitScopeFromSnapshot(snap *shared.SystemSnapshot, dev string) (proxyExitScope, error) {
	var facts struct {
		DefaultIface string                                                              `json:"default_iface"`
		Routes       []struct{ Destination, Gateway, Interface, Scope, Protocol string } `json:"routes"`
	}
	if raw := snap.Detail["routing"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &facts); err != nil {
			return proxyExitScope{}, err
		}
	}
	wans, lans := map[string]bool{}, map[string]bool{}
	if facts.DefaultIface != "" && facts.DefaultIface != dev {
		wans[facts.DefaultIface] = true
	}
	for _, r := range facts.Routes {
		if r.Interface == "" || r.Interface == dev {
			continue
		}
		if r.Destination == "default" || r.Destination == "0.0.0.0/0" || r.Destination == "::/0" {
			wans[r.Interface] = true
			continue
		}
		if r.Gateway != "" || (r.Scope != "link" && r.Protocol != "kernel") {
			continue
		}
		_, network, err := net.ParseCIDR(r.Destination)
		if err != nil {
			continue
		}
		bits, total := network.Mask.Size()
		if bits > 0 && bits < total {
			lans[network.String()] = true
		}
	}
	out := proxyExitScope{}
	for wan := range wans {
		out.WAN = append(out.WAN, wan)
	}
	sort.Strings(out.WAN)
	for lan := range lans {
		out.LAN = append(out.LAN, lan)
	}
	sort.Strings(out.LAN)
	return out, out.validate(dev)
}
