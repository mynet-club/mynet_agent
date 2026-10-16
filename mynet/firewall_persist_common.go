package mynet

// 防火墙持久化 —— **上层合同**（平台无关）。
//
// 为什么需要这一层：平台防火墙 reload/restart 会清掉 `nft add` / `iptables -A`
// 手写的 NAT 规则。隧道建立后必须把「平台自己的配置表达不了的那几条规则」
// 落成一个 **mynet 自有的可重放产物**，等防火墙起回来再重放。
//
// 分层（2026-10-16 裁定）：
//
//	本文件（无构建标签）  规则集 + 两个渲染目标 + 产物路径 —— 所有平台同一套定义
//	firewall_persist_openwrt.go  OpenWrt 执行面：写 sh 产物 + UCI include 登记
//	firewall_persist_linux.go    通用 Linux 执行面：写 sh 产物 + 按后端登记持久化面
//	firewall_persist_stub.go     darwin/windows：空实现，保持调用面统一
//
// 触发点：**隧道接口建立之后**（`service start` 的 verifyTunAfterStart 之后、
// `apply --auto` 尾部），不是 mynetd 启动时 —— mynetd 是纯通信层，不碰系统能力
// （AGENTS.md §1.2）。
//
// 规则内容与 OpenWrt 侧保持一致：NAT masq 双向（oif/iif）。open-port 不在此列 ——
// OpenWrt 由 UCI 原生持久化，不属「重放」范围。
//
// dev 安全契约：一律先过校验（`[A-Za-z0-9._-]{1,15}`），不含空格/引号/分号
// 才允许拼进 sh/脚本内容。

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FirewallIncludeName 重放产物文件名（OpenWrt 侧由 UCI mynet_include 指向它）。
const FirewallIncludeName = "firewall.mynet.include"

// ── mynet 自有表（唯一写入面）────────────────────────────────────
//
// 为什么不用 `ip nat`：那张表归 iptables-nft / ufw 等管理方，`iptables-restore`
// 整表恢复时会把我们装在里面的规则一起冲掉（2026-10-16 本机实测：自有表幸存、
// `ip nat` 里非快照的链被重建）。自有表既躲过整表恢复，又不与别人的规则抢名。
//
// 装进去的（firewallArgv）与被重放的（本文件的两个渲染目标）必须是**同一张表**，
// 否则重放一份、实装另一份 —— 平白多一种「重启后出现两套规则」的形态。
const (
	FirewallNatFamily = "ip"
	FirewallNatTable  = "mynet"
	FirewallNatChain  = "postrouting"
)

// ── proxy 侧自有表（同一套「不归别人管」的纪律）────────────────────
//
// proxy 的规则此前散写在 `ip nat` / `ip filter` —— 那是 iptables-nft / ufw 的
// 管辖面，`iptables-restore` 整表恢复会把它们冲掉（与网关 NAT 同一处实测）。
// 现在全部收进 **`ip mynet_proxy`**（分类器那张表本来就在用它），一条链一个
// 关注点、各组只 flush 自己的链 —— 与 proxy_apply 分类器的幂等纪律同源。
//
// 为什么不并进 `ip mynet`：网关那份产物会 `flush table ip mynet`（整表重声明），
// 并表会把 proxy 规则一起冲掉；两套关注点各自成表、各自幂等，互不干扰。
const (
	// FirewallProxyTable proxy 侧规则的唯一落点（分类器 + DNS + DoT/DoH）。
	FirewallProxyTable = "mynet_proxy"
	// ProxyChainMark 分类器打标（已有，filter/prerouting，priority -150）
	ProxyChainMark = "PREROUTING"
	// ProxyChainDNSRedirect DNS 重定向 DNAT（nat/prerouting）
	ProxyChainDNSRedirect = "dns-redirect"
	// ProxyChainHijackOut 本机 DNS 劫持（nat/output）
	ProxyChainHijackOut = "dns-hijack-out"
	// ProxyChainHijackIn LAN 下游 DNS 劫持（nat/prerouting）
	ProxyChainHijackIn = "dns-hijack-in"
	// ProxyChainDohInput / ProxyChainDohForward DoT/DoH 拦截（filter）
	ProxyChainDohInput   = "doh-input"
	ProxyChainDohForward = "doh-forward"
)

// FirewallIncludePath 重放产物路径。
func FirewallIncludePath(home string) string {
	return filepath.Join(home, "scripts", FirewallIncludeName)
}

// FirewallRuleDir 规则的匹配方向。
type FirewallRuleDir string

const (
	// FirewallDirOut → nft oifname / iptables -o
	FirewallDirOut FirewallRuleDir = "out"
	// FirewallDirIn → nft iifname / iptables -i
	FirewallDirIn FirewallRuleDir = "in"
	// FirewallDirForward → nft iifname + oifname 两向匹配（网关转发语义）
	FirewallDirForward FirewallRuleDir = "forward"
)

// FirewallRuleKind 规则动作。目前只有 masquerade —— open-port 由各平台自己的
// 配置面持久化（OpenWrt UCI），不进重放集。
type FirewallRuleKind string

// FirewallRuleMasq NAT masquerade。
const FirewallRuleMasq FirewallRuleKind = "masquerade"

// FirewallRule 一条「平台防火墙重启后必须仍然存在」的规则（上层合同，平台无关）。
//
// SrcCIDR 是上层留出的源限定位；网关场景**不用它** —— 改用 Dir=Forward 的
// 「iif 隧道 + oif 出口」两向匹配（等价于精确源限定，但不必去猜 overlay 段）。
// 当前 OpenWrt 语义未设源限定，故为空 —— 与既有行为逐字一致，不顺手改生产行为。
type FirewallRule struct {
	Kind FirewallRuleKind
	Dir  FirewallRuleDir
	Dev  string // 入/出接口按 Dir 解释；Forward 时 Dev = 入接口（隧道口）
	// EgressDev 仅 Dir=Forward：出接口（出口网卡）。
	EgressDev string
	Comment   string
	SrcCIDR   string
}

// FirewallReplayRules 上层合同的具体化：隧道建立后必须持久化的规则集。
//
//   - tunDev 非空 → 两条既有语义（oif / iif 各一，OpenWrt 旧 include 同义）
//   - wanDev 非空且 ≠ tunDev → 追加**网关转发 NAT**：iifname tun + oifname wan
//     （「对转发做 nat」的精确写法；不填 wan 时不出这条 —— 入接口单独匹配会把
//     隧道间的流量也 NAT 掉）
//
// ⚠️ wanDev 必须经 firewallResolvedWAN() 取：只有网关意图成立时才会给值。
// 这样「非网关节点」天然没有转发规则，不必在这里再问一次意图（纯核保持纯）。
//
// 空 tunDev 且空 wanDev 返回空集（bundle 未就绪 —— 产物只出骨架，等 sync 补上）。
func FirewallReplayRules(tunDev, wanDev string) ([]FirewallRule, error) {
	if err := validateTunDeviceName(tunDev); err != nil {
		return nil, err
	}
	if err := validateTunDeviceName(wanDev); err != nil {
		return nil, err
	}
	var rules []FirewallRule
	if tunDev != "" {
		rules = append(rules,
			FirewallRule{Kind: FirewallRuleMasq, Dir: FirewallDirOut, Dev: tunDev},
			FirewallRule{Kind: FirewallRuleMasq, Dir: FirewallDirIn, Dev: tunDev},
		)
	}
	if tunDev != "" && wanDev != "" && tunDev != wanDev {
		rules = append(rules, FirewallRule{
			Kind: FirewallRuleMasq, Dir: FirewallDirForward, Dev: tunDev, EgressDev: wanDev,
			Comment: GatewayForwardComment,
		})
	}
	return rules, nil
}

// GatewayForwardComment 网关转发规则的标识注释（实装与重放同用，doctor 靠它辨认）。
const GatewayForwardComment = "mynet-gateway-forward"

// firewallWANResolver 解析**当前机器**的默认出接口（oif）。
// 包级变量是为了可测：纯函数侧一律显式传参，只有入口处才允许问系统。
var firewallWANResolver = resolveDefaultWANDevice

// firewallListenPortResolver 回答本节点的 listen 端口（open-port 意图用）。
// 来源：bundle 的 node.listen_port（云端已知），解析不到返回 0 = 不放行。
var firewallListenPortResolver = resolveListenPort

// firewallGatewayResolver 回答「本节点是不是网关」（意图来自 envelope 的
// network.firewall，由 BundleEnvelopePublisher 按 Node::isGateway 产出）。
// 同样是包级变量：纯核里不读 bundle，只有入口问一次。
var firewallGatewayResolver = resolveGatewayIntent

// firewallResolvedWAN 出接口**只在网关意图成立时才解析**。
// 非网关节点拿 wan 会得出「iif 隧道 + oif 出口」的转发 NAT 规则 —— 那不是它的职责，
// 所以用「wan 有值 ⇔ 意图成立」把两件事绑在一起（见 FirewallReplayRules 的注释）。
func firewallResolvedWAN() string {
	if !firewallGatewayResolver() {
		return ""
	}
	return firewallWANResolver()
}

// FirewallIncludeScript 渲染目标 A：sh 重放脚本（被平台的防火墙/钩子调用）。
//
// 两个平台共用同一形状：OpenWrt 由 fw4 的 include 段调用，通用 Linux 由探测到的
// 后端（netfilter-persistent 等）调用。幂等：先查后加，重复执行不堆规则。
func FirewallIncludeScript(tunDev string) (string, error) {
	// 意图与出接口都是机器事实，只在入口问一次；纯核里一律显式传参（可测）。
	return firewallIncludeScript(tunDev, firewallResolvedWAN())
}

// firewallIncludeScript 纯核：已知 tun/wan 时渲染 sh 重放脚本。
func firewallIncludeScript(tunDev, wanDev string) (string, error) {
	rules, err := FirewallReplayRules(tunDev, wanDev)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# mynet firewall include —— 由 mynet 生成，勿手改。\n")
	b.WriteString("# 职责：防火墙 reload/restart 后重放 mynet 手写的 NAT 规则。\n")
	b.WriteString("# 丢了这个文件 = 防火墙一重启规则就丢（持久化机制必须保留）。\n")
	b.WriteString("# 接口名与驱动投影同源；变更由 `mynet firewall sync` 对齐。\n")
	fmt.Fprintf(&b, "DEV=%q\n", tunDev)
	fmt.Fprintf(&b, "WAN=%q\n", wanDev)
	b.WriteString("[ -n \"$DEV\" ] || exit 0\n")
	b.WriteString("command -v nft >/dev/null 2>&1 || exit 0\n\n")
	// 表/链与 firewallArgv 装的是同一张（FirewallNatTable）—— 重放的就是实装的那条。
	fmt.Fprintf(&b, "nft list table %s %s >/dev/null 2>&1 || nft add table %s %s\n",
		FirewallNatFamily, FirewallNatTable, FirewallNatFamily, FirewallNatTable)
	fmt.Fprintf(&b, "nft list chain %s %s %s >/dev/null 2>&1 || "+
		"nft add chain %s %s %s '{ type nat hook postrouting priority srcnat; }'\n",
		FirewallNatFamily, FirewallNatTable, FirewallNatChain,
		FirewallNatFamily, FirewallNatTable, FirewallNatChain)
	for _, r := range rules {
		match := nftRuleMatch(r)
		// 幂等 grep 只认匹配式本身 —— 注释可有可无，两边都不因此重复加。
		fmt.Fprintf(&b, "nft list chain %s %s %s 2>/dev/null | grep -q %s || nft add rule %s %s %s %s%s\n",
			FirewallNatFamily, FirewallNatTable, FirewallNatChain,
			shDoubleQuote(match),
			FirewallNatFamily, FirewallNatTable, FirewallNatChain, match, nftCommentSuffix(r))
	}
	// 加载**其余 nft 产物**（集合元素 + proxy 规则）—— fw4 只会跑本脚本，
	// 所以「防火墙重启后完整重放」的义务全在这条循环里：规则 + 集合一起回来。
	// 不加这段的话：fw4 reload 后网关规则在、proxy 集合空、分流静默失效。
	b.WriteString("\n# 重放 scripts/ 下的 nft 产物（集合 + proxy 规则）\n")
	b.WriteString("for f in \"$(dirname \"$0\")\"/*.nft; do\n")
	b.WriteString("  [ -f \"$f\" ] && nft -f \"$f\"\n")
	b.WriteString("done\n")
	b.WriteString("exit 0\n")
	return b.String(), nil
}

// nftRuleMatch 规则的**完整 nft 规则式**（含 masquerade，不含 comment）。
// 设备名走 $DEV/$WAN 变量，供 sh 重放用。
func nftRuleMatch(r FirewallRule) string {
	return nftMatchExpr(r, "$DEV", "$WAN") + " masquerade"
}

// nftRuleLiteral 规则的**完整 nft 字面式**（含 masquerade 与 comment），
// 设备名写成常量，供 `nft -f` 加载。
func nftRuleLiteral(r FirewallRule) string {
	return nftMatchExpr(r, r.Dev, r.EgressDev) + " masquerade" + nftCommentSuffix(r)
}

// nftCommentSuffix 注释后缀（无注释时为空串，避免出现尾空格/裸 comment）。
func nftCommentSuffix(r FirewallRule) string {
	if r.Comment == "" {
		return ""
	}
	return fmt.Sprintf(" comment %q", r.Comment)
}

// nftMatchExpr 按 Dir 拼匹配式（不含 masquerade 动作与 comment）。
// devVar = 入/单向设备的字面形式（sh 传 "$DEV"、nft 文件传 "gnb_tun"）；
// wanVar = Forward 的出接口字面形式（其他 Dir 不用）。
// 设备名一律**包进双引号**：sh 侧要 "$DEV" 展开、nft 侧 `oifname "gnb_tun"` 也合法，
// 两边同一形状 —— 去掉引号会让 grep 幂等判据与实装规则对不上，导致重复加。
func nftMatchExpr(r FirewallRule, devVar, wanVar string) string {
	var parts []string
	if r.SrcCIDR != "" {
		parts = append(parts, "ip saddr", r.SrcCIDR)
	}
	devQ := `"` + devVar + `"`
	wanQ := `"` + wanVar + `"`
	switch r.Dir {
	case FirewallDirIn:
		parts = append(parts, "iifname", devQ)
	case FirewallDirForward:
		parts = append(parts, "iifname", devQ, "oifname", wanQ)
	default: // out：oif 用的也是**本规则的 Dev**（不是 wan）
		parts = append(parts, "oifname", devQ)
	}
	return strings.Join(parts, " ")
}

// shDoubleQuote 把待匹配文本包进 sh 双引号（`"` → `\"`），供 grep -q 用。
func shDoubleQuote(s string) string {
	return "\"" + strings.ReplaceAll(s, `"`, `\"`) + "\""
}

// FirewallNftRules 渲染目标 B：nft 文件内容（被 `nft -f` 加载）。
//
// 用 **mynet 自有表** `ip mynet`，不动别人的表：Linux 上 iptables-nft 也在用
// `table ip nat`，再声明一次会撞（File exists），而 NAT 链允许按优先级并存。
func FirewallNftRules(tunDev string) (string, error) {
	return firewallNftRules(tunDev, firewallResolvedWAN(), firewallListenPortResolver())
}

// firewallNftRules 纯核：已知 tun/wan/listenPort 时渲染 nft 文件内容。
//
// listenPort > 0 时在本表再开一条 **input 过滤链**放行该 UDP 端口 ——
// open-port 也是规则本体，不进 Go（裁定）；doctor 的机测是全 ruleset 扫
// `udp dport <port> accept`，放自有表照样被它认出来。
func firewallNftRules(tunDev, wanDev string, listenPort int) (string, error) {
	rules, err := FirewallReplayRules(tunDev, wanDev)
	if err != nil {
		return "", err
	}
	if len(rules) == 0 && listenPort <= 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("# mynet firewall rules —— 由 mynet 生成，勿手改。\n")
	b.WriteString("# 职责：nftables 加载（reload/restart）后 NAT masq 仍在。\n")
	b.WriteString("# 用 mynet 自有表，不与 iptables-nft 的 `table ip nat` 抢名。\n")
	//
	// ⚠️ 前导 `add table` + `flush table` 是**幂等的地基**，缺一不可：
	//   · 只有 body：`nft -f` 重复加载 = 规则重复（2026-10-16 本机实测：加载 3 次 → 3 条）
	//   · `flush table` 单独放最前：表还不存在时整份文件事务性失败（nft -f 见错即中止），
	//     所以必须先 `add table` 把表建出来
	//   · 加上前导后：3 次加载恒为 1 条（同日实测）
	// 这就是「Go 只写产物 + 委托 nft -f 加载」能替代 Go 侧 flush+add 的前提。
	fmt.Fprintf(&b, "add table %s %s\n", FirewallNatFamily, FirewallNatTable)
	fmt.Fprintf(&b, "flush table %s %s\n", FirewallNatFamily, FirewallNatTable)
	fmt.Fprintf(&b, "table %s %s {\n", FirewallNatFamily, FirewallNatTable)
	fmt.Fprintf(&b, "\tchain %s {\n", FirewallNatChain)
	b.WriteString("\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	for _, r := range rules {
		fmt.Fprintf(&b, "\t\t%s\n", nftRuleLiteral(r))
	}
	b.WriteString("\t}\n")
	if listenPort > 0 {
		// open-port：listen 端口放行（gNB 入站）。规则本体不进 Go，随产物重放。
		fmt.Fprintf(&b, "\tchain input {\n")
		b.WriteString("\t\ttype filter hook input priority 0; policy accept;\n")
		fmt.Fprintf(&b, "\t\tudp dport %d accept comment %q\n", listenPort, ListenPortComment)
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// ListenPortComment open-port 规则的标识注释（doctor 辨认用）。
const ListenPortComment = "mynet-listen"

// ── iptables 回退路径的自有链（与 nft 自有表同一纪律）──────────────
//
// iptables 没有「自有表」，但有**自有链**：规则只进自有链、共享链只留一条跳转。
// 幂等地基 = `-N`（已存在即跳过）+ `-F` 自有链 + 重加 + 跳转用 `-C || -I` 守卫。
// 2026-10-17 本机实测：纯 `-A` 连跑 3 次 = 2→4→6 条堆积；own chain + flush = 恒 2。
//
// ⚠️ 清理时**只能 flush 自有链**，绝不 `-F INPUT`/`-F OUTPUT` —— 那是 ufw/Docker
// 的共享链，刷了会把别人的规则一起清掉（DNSHijack cleanup 曾经就是这么写的）。
const (
	IPTablesChainDohIn  = "MYNET_DOH_IN"
	IPTablesChainDohFwd = "MYNET_DOH_FWD"
	IPTablesChainDnsOut = "MYNET_DNS_OUT"
	IPTablesChainDnsPre = "MYNET_DNS_PRE"
)

// IptOwnChainCmds 幂等地基：建自有链（已存在跳过）+ 清空，随后调用方重加规则。
func IptOwnChainCmds(table, chain string) []routeCmd {
	base := []string{"iptables"}
	if table != "" {
		base = append(base, "-t", table)
	}
	return []routeCmd{
		{argv: append(append([]string{}, base...), "-N", chain), optional: true},
		{argv: append(append([]string{}, base...), "-F", chain), optional: true},
	}
}

// IptJumpCmd 幂等跳转：`-C` 查到即跳过，查不到才 `-I 1` 插入。
// 用 sh -c 是因为「查不到才插」是一条命令表达不了的条件语义；
// 无守卫的 `-A <parent> -j <chain>` 会随每次 apply 堆一条跳转。
func IptJumpCmd(table, parent, chain string) routeCmd {
	base := []string{"iptables"}
	if table != "" {
		base = append(base, "-t", table)
	}
	check := strings.Join(append(append([]string{}, base...), "-C", parent, "-j", chain), " ")
	insert := strings.Join(append(append([]string{}, base...), "-I", parent, "1", "-j", chain), " ")
	return routeCmd{argv: []string{"sh", "-c", check + " || " + insert}, note: "幂等跳转"}
}

// IptUnjumpCmd 幂等摘除跳转（清理用；没插过不算失败）。
func IptUnjumpCmd(table, parent, chain string) routeCmd {
	base := []string{"iptables"}
	if table != "" {
		base = append(base, "-t", table)
	}
	return routeCmd{
		argv:     append(append([]string{}, base...), "-D", parent, "-j", chain),
		optional: true,
	}
}

// validateTunDeviceName 与 domain.ValidateTunDevice 同契约；本包内短名防传错层。
func validateTunDeviceName(dev string) error {
	if dev == "" {
		return nil
	}
	if len(dev) > 15 {
		return fmt.Errorf("接口名 %q 超长（≤15）", dev)
	}
	for _, r := range dev {
		ok := r == '.' || r == '_' || r == '-' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return fmt.Errorf("接口名 %q 含非法字符", dev)
		}
	}
	return nil
}

// ── 持久化面选择（纯函数，平台探测结果由执行面注入）──────────────────
//
// 通用 Linux 没有像 fw4 那样的「设计上会调用 include」的钩子，所以必须先判
// **谁在管这台机器的防火墙**，再登记到它自己的持久化面上；登记不到就说清楚
// （skip ≠ pass），而不是另起一套（if-up.d + iptables-restore 那类做法会与
// 系统既有机制互相 flush，反而制造瞬断）。

// FirewallBackend 持久化面。
type FirewallBackend string

const (
	// FirewallBackendFw4 OpenWrt fw4：设计上就有 include 段，登记 UCI mynet_include
	FirewallBackendFw4 FirewallBackend = "fw4"
	// FirewallBackendFirewalld firewalld（direct.xml 承载）
	FirewallBackendFirewalld FirewallBackend = "firewalld"
	// FirewallBackendUfw ufw（before.rules 承载）
	FirewallBackendUfw FirewallBackend = "ufw"
	// FirewallBackendNftablesD nftables.service + /etc/nftables.conf 含 nftables.d
	FirewallBackendNftablesD FirewallBackend = "nftables-d"
	// FirewallBackendIPtablesPersistent netfilter-persistent（保存运行期规则集）
	FirewallBackendIPtablesPersistent FirewallBackend = "iptables-persistent"
	// FirewallBackendNone 无可用持久化面
	FirewallBackendNone FirewallBackend = "none"
)

// FirewallBackendFacts 平台探测结果（执行面填，选择逻辑纯函数）。
type FirewallBackendFacts struct {
	FirewalldActive     bool
	UfwActive           bool
	NftablesUnitPresent bool
	// NftablesConfIncludesD /etc/nftables.conf 真的 include 了 /etc/nftables.d/
	// —— 目录存在但没人 include 时写进去等于没写（本机实测就是这种情况）。
	NftablesConfIncludesD bool
	NetfilterPersistent   bool
}

// selectFirewallBackend 按「谁在管防火墙」选持久化面。
//
// 顺序即语义：active 的守护进程优先（它 reload 时会重建自己的规则集，别人的
// 东西会被清掉）；其次才是「没人管、但有现成的加载面」。
func selectFirewallBackend(f FirewallBackendFacts) FirewallBackend {
	switch {
	case f.FirewalldActive:
		return FirewallBackendFirewalld
	case f.UfwActive:
		return FirewallBackendUfw
	case f.NftablesUnitPresent && f.NftablesConfIncludesD:
		return FirewallBackendNftablesD
	case f.NetfilterPersistent:
		return FirewallBackendIPtablesPersistent
	default:
		return FirewallBackendNone
	}
}

// FirewallNftArtifactName mynet 自有 nft 产物文件名（放 $MYNET_HOME/scripts/）。
//
// 与 include（sh）**并存**：sh 那份是给「只能跑脚本的钩子」用的（fw4 的 include 段），
// nft 这份是给**系统原生加载器** `nft -f` 用的 —— Go 不 exec `nft add rule`，
// 只把文件交给 nft 加载（裁定：规则本体不进 Go）。
const FirewallNftArtifactName = "firewall.mynet.nft"

// FirewallNftPath nft 产物路径。
func FirewallNftPath(home string) string {
	return filepath.Join(home, "scripts", FirewallNftArtifactName)
}

// ErrNoPersistenceSurface 「没有可用的防火墙持久化面」哨兵。
//
// 它**不阻断**写产物与当次加载 —— 丢的只是「防火墙重启后自动重放」能力，
// 当次生效不受影响。调用方必须把它当**告警**而不是失败（skip ≠ pass，
// 但也不该因为登记不了就不装规则）。
var ErrNoPersistenceSurface = errors.New(
	"无可用的防火墙持久化面（规则当次可装，但防火墙重启后不会自动重放）")

// WriteFirewallNftArtifact 写 nft 产物到 $MYNET_HOME/scripts/（共享，平台无关）。
// dev 为空时清掉旧文件，免得留着上一版接口名的规则。
func WriteFirewallNftArtifact(home, tunDev string) error {
	path := FirewallNftPath(home)
	body, err := FirewallNftRules(tunDev)
	if err != nil {
		return err
	}
	if body == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清 nft 产物: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("建 scripts 目录: %w", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("写 nft 产物: %w", err)
	}
	return nil
}

// FirewallProxyNftName proxy 侧规则产物文件名（放 $MYNET_HOME/scripts/）。
//
// 与网关那份（FirewallNftArtifactName）**分开**：proxy 表里有 1.7 万条集合元素，
// 产物只能 `flush chain` 不能 `flush table`（清表会连集合一起清空，见
// proxy_apply.go 第 0 步的实测）；而网关产物是整表 flush + 重声明。两套幂等
// 纪律不同，合并成一份文件会互相破坏。
const FirewallProxyNftName = "firewall.mynet.proxy.nft"

// FirewallProxyNftPath proxy 规则产物路径。
func FirewallProxyNftPath(home string) string {
	return filepath.Join(home, "scripts", FirewallProxyNftName)
}

// FirewallProxySetNftName proxy **集合元素**产物文件名。
//
// 集合是数据不是规则，但**必须跟规则一起被重放**：防火墙 reload 后
// `flush chain` 只清规则、集合被保住，可 `nft flush ruleset` / 重启会连集合
// 一起没 —— 规则回来了而集合为空 = 打标 0 命中 = 分流静默失效（fail-open）。
// 把元素也落成产物，同一加载路径就能一并恢复，不需要额外的补给 unit。
const FirewallProxySetNftName = "firewall.mynet.proxy-set.nft"

// FirewallProxySetNftPath proxy 集合产物路径。
func FirewallProxySetNftPath(home string) string {
	return filepath.Join(home, "scripts", FirewallProxySetNftName)
}

// PersistProxySetArtifact 把**集合片段**（WriteNftSetFragment 的产物）收进
// scripts/，成为可重放产物的一员。内容含 `flush set` + `add element`，
// 重载会整集重建 —— 与「只 flush chain 的规则产物」互补，不冲突。
func PersistProxySetArtifact(home, setFragmentPath string) error {
	if setFragmentPath == "" {
		return nil
	}
	body, err := os.ReadFile(setFragmentPath)
	if err != nil {
		return fmt.Errorf("读集合片段: %w", err)
	}
	dst := FirewallProxySetNftPath(home)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("建 scripts 目录: %w", err)
	}
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		return fmt.Errorf("写集合产物: %w", err)
	}
	return nil
}

// NftBatchFromCmds 把 `nft …` 的 argv 序列转成 `nft -f` 可加载的文本。
//
// 为什么能这样转：nft 的 CLI 语法与 `-f` 文件语法对 add table/chain/set/rule、
// flush chain 是一致的 —— 去掉首词 `nft`，其余原样成行即可。
// 唯一要补的是 **iifname/oifname 后的裸设备名补双引号**：文件语法里裸标识会当
// 表达式解析，CLI 里却允许不带引号。
//
// 非 nft 命令（ip/iptables/sysctl/mynet…）会被跳过；全部跳过时返回 ok=false，
// 调用方据此决定「要不要生成产物」。
func NftBatchFromCmds(cmds []routeCmd) (string, bool) {
	var b strings.Builder
	b.WriteString("# generated by mynet — do not edit\n")
	n := 0
	for _, c := range cmds {
		if len(c.argv) < 2 || c.argv[0] != "nft" {
			continue
		}
		b.WriteString(nftLineFromArgv(c.argv[1:]))
		b.WriteString("\n")
		n++
	}
	if n == 0 {
		return "", false
	}
	return b.String(), true
}

// SplitNftCmds 把一批命令拆成「nft 规则批次」与「照旧执行的其余」。
// 规则批次走产物 + nft -f；其余（ip route / ip rule / iptables / sysctl /
// mynet 子命令 / ipset）不在「规则本体」范畴，维持原执行方式。
func SplitNftCmds(cmds []routeCmd) (string, []routeCmd) {
	batch, ok := NftBatchFromCmds(cmds)
	rest := make([]routeCmd, 0, len(cmds))
	for _, c := range cmds {
		if len(c.argv) > 0 && c.argv[0] == "nft" {
			continue
		}
		rest = append(rest, c)
	}
	if !ok {
		return "", cmds
	}
	return batch, rest
}

// nftLineFromArgv argv[1:] → 一行 nft 文件语法。
func nftLineFromArgv(parts []string) string {
	var out []string
	for i, tok := range parts {
		// iifname/oifname 后紧跟的设备名：文件语法要带引号
		if i > 0 && (parts[i-1] == "iifname" || parts[i-1] == "oifname") {
			out = append(out, `"`+strings.Trim(tok, `"`)+`"`)
			continue
		}
		out = append(out, tok)
	}
	return strings.Join(out, " ")
}

// WriteFirewallProxyArtifact 把 proxy 规则批次写成产物（供 nft -f 加载 / 重启后重放）。
func WriteFirewallProxyArtifact(home, batch string) error {
	if batch == "" {
		return nil
	}
	path := FirewallProxyNftPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("建 scripts 目录: %w", err)
	}
	if err := os.WriteFile(path, []byte(batch), 0o644); err != nil {
		return fmt.Errorf("写 proxy 产物: %w", err)
	}
	return nil
}

// LoadNftArtifacts 委托系统加载器：把 $MYNET_HOME/scripts/ 下的 **每个 nft 产物**
// 交给 `nft -f`。这是「Go 只写产物 + 委托系统加载」的统一出口 —— 网关与 proxy
// 各一份产物，加载方式完全相同。
func LoadNftArtifacts(home string) error {
	if !commandExists("nft") {
		return errors.New("系统无 nft，无法加载防火墙产物")
	}
	dir := filepath.Join(home, "scripts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("读 scripts 目录: %w", err)
	}
	loaded := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".nft") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if out, err := exec.Command("nft", "-f", path).CombinedOutput(); err != nil {
			return fmt.Errorf("nft -f %s: %w (%s)", path, err, strings.TrimSpace(string(out)))
		}
		loaded++
	}
	if loaded == 0 {
		return fmt.Errorf("scripts/ 下无 nft 产物（先 mynet firewall sync / apply）")
	}
	return nil
}

// FirewallNftDropInPath nftables 加载面里的 mynet 自有文件。
func FirewallNftDropInPath() string { return "/etc/nftables.d/mynet.nft" }
