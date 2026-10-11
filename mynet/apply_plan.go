package mynet

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/logger"
)

// ApplyPlan 是 L6 写入计划：只含系统网络语义，不含驱动原生语法。
// 跨驱动互通（A gnb → C 双驱动 → B wg）就是这里的几条 Route，驱动层互不知晓对方。
//
// SSOT（route-apply-unification.md）：OS 路由的数据源是 bundle.network.routes
// （back 从 node_ips 投影），**不是** gnb 的 route.conf —— 后者是 L5 地址宣告，
// 不进内核。平台只分叉 routeCmds 的命令方言，策略全在 executeApply。
type ApplyPlan struct {
	// 保留本节点隧道名，即使撤销后无 Proxy/Routes，也能精确清理出口规则。
	ProxyExitDevice string       `json:"proxy_exit_device,omitempty"`
	LegacyProxyExit bool         `json:"legacy_proxy_exit,omitempty"`
	Routes          []RouteOp    `json:"routes,omitempty"`
	Firewall        []FirewallOp `json:"firewall,omitempty"`
	Interfaces      []IfaceOp    `json:"interfaces,omitempty"`
	// Proxy 代理分流（主线⑦）。非 nil 时 executeApply 追加策略路由/DNS 命令。
	Proxy     *ProxyApplyPlan `json:"proxy,omitempty"`
	ExtraExec []ExecOp        `json:"extra_exec,omitempty"` // 兜底；优先结构化字段
	// LocalAddresses 本机 overlay 地址（如 10.0.0.15/32）。
	// 用途只有一个：bundle 里的 Device 是**逻辑名**（gnb_tun），而 darwin 的 gnb
	// 会拿到内核分配的 utunN（gnb_drv_darwin.c:249-252 把 ifname 覆盖成
	// UTUN_OPT_IFNAME）。执行期用这里的 IP 反查真实接口，否则路由装到
	// 一个不存在的 gnb_tun 上 —— 症状是「隧道起了、IP 有了、ping 全丢」。
	LocalAddresses []string `json:"local_addresses,omitempty"`
	// ProxyOnly 只跑 proxy 段（本地开/关代理用）：跳过 OS 路由/防火墙/接口与钉默认网关。
	// 不是 plan 文件字段（json:"-"），只由 `mynet apply --proxy-only` 在内存里置位。
	ProxyOnly bool `json:"-"`
}

type RouteOp struct {
	Destination string `json:"destination"` // 10.8.0.0/24
	Gateway     string `json:"gateway,omitempty"`
	Device      string `json:"device"` // gnb0 / wg0 —— 对 apply 无差别
	Metric      int    `json:"metric,omitempty"`
	Action      string `json:"action"` // add | replace | del
}

type FirewallOp struct {
	Table   string `json:"table,omitempty"`
	Chain   string `json:"chain,omitempty"`
	Action  string `json:"action"` // ensure-masq | open-port | …
	Device  string `json:"device,omitempty"`
	Port    int    `json:"port,omitempty"`
	Proto   string `json:"proto,omitempty"`
	Comment string `json:"comment,omitempty"`
}

type IfaceOp struct {
	Name   string `json:"name"`
	Action string `json:"action"` // up | down
	MTU    int    `json:"mtu,omitempty"`
}

type ExecOp struct {
	// GUARD-OK 逃生舱：仅当结构化字段无法表达时使用；优先改计划结构
	Argv []string `json:"argv"`
}

func readApplyPlan(path string) (*ApplyPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p ApplyPlan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// routeCmd 是一条待执行的路由命令。optional 表示失败可忽略 ——
// BSD 没有 `ip route replace`，replace 只能「先删后加」，而 delete
// 在路由本来就不存在时会失败，那不是错误。
type routeCmd struct {
	argv     []string
	optional bool
	// note 仅诊断用（如「macOS 跳过 fwmark」），不参与执行
	note string
}

// applyJournal 记录**本轮真的落下去**的操作，用于失败时回滚。
//
// 只收非 dry 的成功 op：dry-run 没动系统，也就没有可回滚的东西。
// 这是「新配置应用失败不得清除旧配置」（MYNET_V1_PLAYBOOK §6）的落点 ——
// 回滚只撤本轮新加的，旧一代的路由/规则原样留在系统里。
type applyJournal struct {
	routes   []RouteOp
	firewall []FirewallOp
	ifaces   []IfaceOp
}

func (j *applyJournal) recordRoute(r RouteOp)       { j.routes = append(j.routes, r) }
func (j *applyJournal) recordFirewall(f FirewallOp) { j.firewall = append(j.firewall, f) }
func (j *applyJournal) recordIface(i IfaceOp)       { j.ifaces = append(j.ifaces, i) }

func (j *applyJournal) empty() bool {
	return len(j.routes) == 0 && len(j.firewall) == 0 && len(j.ifaces) == 0
}

// executeApply 按 system.json 能力选择工具执行。唯一写入入口（F-5）。
//
// 路由三道闸（对齐 openwrt node.lua:clear_routes / route_policy.sh）：
//  1. 滤掉与本机直连网段冲突的条目（防路由劫持）
//  2. **只删自己装的**：replace 的「先删后加」前先查目的地现挂在谁的设备上，
//     不是我们的隧道口就整条跳过 —— 系统直连/别人的静态路由碰都不碰。
//     查的是 FIB 里**前缀完全相等**那条（destOwnerDev），不是路由查询结果：
//     隧道路由被清空后默认路由会替任何目的地答一个物理口，那不算别人占有，
//     否则我们自己的路由永远装不回去（见 apply_osroute_unix.go）
//  3. 网关被隧道盖住时才钉回物理口（fix_wan_gateway_route 同款，先查再钉）
func executeApply(snap *shared.SystemSnapshot, plan *ApplyPlan, dry bool) int {
	log := logger.Comp("apply")
	log.Info("apply 开始",
		"routes", len(plan.Routes), "firewall", len(plan.Firewall), "ifaces", len(plan.Interfaces),
		"execs", len(plan.ExtraExec), "dry", dry)
	fail := 0
	var applied applyJournal
	localNets := localConnectedNets()
	var routes []RouteOp
	for _, r := range plan.Routes {
		if n := parseDestNet(r.Destination); n != nil && overlapsAny(n, localNets) {
			fmt.Fprintf(os.Stderr, "skip route %s: 与本机直连网段冲突（防路由劫持）\n", r.Destination)
			log.Warn("跳过路由（与直连网段冲突）", "dest", r.Destination)
			continue
		}
		routes = append(routes, r)
	}
	for _, r := range routes {
		r.Device = resolveDevice(r.Device, plan.LocalAddresses)
		if destOwnerDev(r.Destination) != "" && !destOwnedBy(r.Destination, r.Device) {
			fmt.Fprintf(os.Stderr, "skip route %s: 现挂在 %s，不是我们装的（只删自己配置的）\n",
				r.Destination, destOwnerDev(r.Destination))
			log.Warn("跳过路由（非本配置安装）", "dest", r.Destination, "owner", destOwnerDev(r.Destination))
			continue
		}
		ok := true
		for _, c := range routeCmds(snap, r) {
			if err := runOp(dry, c.argv); err != nil {
				if c.optional {
					continue
				}
				fmt.Fprintf(os.Stderr, "route %s %s: %v\n", r.Action, r.Destination, err)
				log.Error("路由失败", "action", r.Action, "dest", r.Destination, "err", err)
				fail = 1
				ok = false
			}
		}
		if ok && !dry {
			applied.recordRoute(r)
		}
	}
	if !plan.ProxyOnly {
		if err := pinDefaultGateway(snap, dry); err != nil {
			fmt.Fprintf(os.Stderr, "pin default gateway: %v\n", err)
			log.Warn("钉默认网关失败", "err", err)
			fail = 1
		}
	}
	for _, f := range plan.Firewall {
		argv := firewallArgv(snap, f)
		if len(argv) == 0 {
			fmt.Fprintf(os.Stderr, "firewall %s: 当前 system.json 无可用工具\n", f.Action)
			log.Warn("防火墙无可用工具", "action", f.Action)
			fail = 1
			continue
		}
		if err := runOp(dry, argv); err != nil {
			fmt.Fprintf(os.Stderr, "firewall %s: %v\n", f.Action, err)
			log.Error("防火墙失败", "action", f.Action, "err", err)
			fail = 1
			continue
		}
		if !dry {
			applied.recordFirewall(f)
		}
	}
	for _, i := range plan.Interfaces {
		argv := ifaceArgv(snap, i)
		if err := runOp(dry, argv); err != nil {
			fmt.Fprintf(os.Stderr, "iface %s %s: %v\n", i.Action, i.Name, err)
			log.Error("接口操作失败", "action", i.Action, "name", i.Name, "err", err)
			fail = 1
			continue
		}
		if !dry {
			applied.recordIface(i)
		}
	}
	if plan.Proxy != nil {
		// 代理分流（L6）：先取 IP 列表并生成 set 片段，再跑策略路由/DNS
		rt := plan.Proxy.Runtime
		rt.TunDevice = resolveDevice(rt.TunDevice, plan.LocalAddresses)
		if rt.IPSetFile != "" {
			// 有 ref（云端解算的列表源）才需要先取；**本地回落文件**（随包 proxy_sources）
			// 直接用盘上的，不再要求 ref —— 否则集合为空、打标匹配不到任何目的
			// （2026-10-11 .182 实测：集合 0 条）。
			fetchOK := true
			if ref := proxyIPListRef(plan.Proxy.Policy); ref != nil {
				if _, err := FetchProxyIPList(ref, rt.IPSetFile, nil); err != nil {
					fmt.Fprintf(os.Stderr, "proxy ip list: %v\n", err)
					log.Error("IP 列表获取失败", "err", err)
					fail = 1
					fetchOK = false
				}
			}
			if fetchOK {
				if snap.Capabilities["nft"] {
					if err := WriteNftSetFragment(rt.IPSetFile); err != nil {
						fmt.Fprintf(os.Stderr, "proxy nft fragment: %v\n", err)
						fail = 1
					}
				} else if snap.Capabilities["iptables"] {
					if err := WriteIPSetRestore(rt.IPSetFile); err != nil {
						fmt.Fprintf(os.Stderr, "proxy ipset restore: %v\n", err)
						fail = 1
					}
				}
				for _, c := range LoadIPSetFileCmds(snap, rt.IPSetFile) {
					if err := runOp(dry, c.argv); err != nil {
						if c.optional {
							continue
						}
						fmt.Fprintf(os.Stderr, "proxy load set %v: %v\n", c.argv, err)
						fail = 1
					}
				}
			}
		}
		// P0-b：写 DNS 产物（网关 dnsmasq 片段 / PC resolv）
		dnsForm := DNSFormGateway
		if plan.Proxy.Policy.DNS != nil && plan.Proxy.Policy.DNS.Hijack == "local" {
			// 只劫本机时按 PC 形态配上游
			dnsForm = DNSFormPC
		}
		if plan.Proxy.Policy.DomainRules != nil || (plan.Proxy.Policy.DNS != nil && plan.Proxy.Policy.DNS.Hijack != "" && plan.Proxy.Policy.DNS.Hijack != "off") {
			reloads, derr := ApplyProxyDNSFiles(plan.Proxy.Policy, dnsForm, rt.IPSetFile+".dnsmasq.conf", "")
			if derr != nil {
				fmt.Fprintf(os.Stderr, "proxy dns files: %v\n", derr)
				fail = 1
			}
			for _, rc := range reloads {
				_ = runOp(dry, rc) // reload 失败不阻断 apply
			}
		}
		// P0-c：动态 ipset（DNS 回答钩子写入的文件）增量入集
		if dyn, err := LoadDynamicIPSetFile(DynamicIPSetFilePath("")); err == nil && len(dyn) > 0 {
			for _, c := range DynamicIPSetCmds(snap, dyn, "", "", "") {
				if err := runOp(dry, c.argv); err != nil && !c.optional {
					fail = 1
				}
			}
		}
		pcmds := proxyAllCmds(snap, plan.Proxy.Policy, rt)
		log.Info("proxy apply", "cmds", len(pcmds),
			"role", plan.Proxy.Policy.Role,
			"match", func() string {
				if plan.Proxy.Policy.Split != nil {
					return plan.Proxy.Policy.Split.MatchMode
				}
				return ""
			}())
		for _, c := range pcmds {
			if err := runOp(dry, c.argv); err != nil {
				if c.optional {
					continue
				}
				fmt.Fprintf(os.Stderr, "proxy %v: %v\n", c.argv, err)
				log.Error("proxy 命令失败", "argv", strings.Join(c.argv, " "), "err", err)
				fail = 1
			}
		}
	} else if st, err := LoadProxyState(); err == nil && st != nil && (st.Local != nil || st.Cloud != nil) {
		// proxy 已禁用，但本机曾管理过它（intent 有记录）：撤销历史钉扎。
		// 不清理的话，禁用后国外 DNS /32 仍指向已消失的隧道 —— 实测症状是
		// 整机 DNS 黑洞。清理命令全部 optional：没装过/已失效属正常。
		var cleanupPolicy domain.ProxyPolicy
		if eff := st.Effective(); eff != nil {
			cleanupPolicy = eff.Policy
		} else if st.Local != nil {
			cleanupPolicy = st.Local.Policy // 停用后仍用原 mark/table 清理。
		}
		tunDev := plan.ProxyExitDevice
		if tunDev == "" {
			for _, r := range plan.Routes {
				if r.Device != "" {
					tunDev = r.Device
					break
				}
			}
		}
		crt := ProxyRuntime{TunDevice: resolveDevice(tunDev, plan.LocalAddresses)}
		cleanup := proxyCleanupCmds(snap, &cleanupPolicy, crt)
		if len(cleanup) > 0 {
			log.Info("proxy cleanup（已禁用）", "cmds", len(cleanup))
		}
		for _, c := range cleanup {
			if err := runOp(dry, c.argv); err != nil && !c.optional {
				fmt.Fprintf(os.Stderr, "proxy cleanup %v: %v\n", c.argv, err)
				fail = 1
			}
		}
	}
	for _, e := range plan.ExtraExec {
		if err := runOp(dry, e.Argv); err != nil {
			fmt.Fprintf(os.Stderr, "exec %v: %v\n", e.Argv, err)
			log.Error("附加命令失败", "argv", strings.Join(e.Argv, " "), "err", err)
			fail = 1
		}
	}

	// 路由/client 应用成功后再提交出口专表；此前失败时保留上一代出口规则。
	exitDev := plan.ProxyExitDevice
	var exitPolicy *domain.ProxyPolicy
	if plan.Proxy != nil {
		exitPolicy = plan.Proxy.Policy
		if exitDev == "" {
			exitDev = plan.Proxy.Runtime.TunDevice
		}
	}
	if fail == 0 && exitDev != "" {
		exitDev = resolveDevice(exitDev, plan.LocalAddresses)
		if err := syncProxyExitFirewall(snap, exitPolicy, exitDev, plan.LegacyProxyExit,
			func(argv []string, input string) (string, error) { return runProxyFirewall(dry, argv, input) }); err != nil {
			log.Error("出口 NAT/ACL 应用或清理失败", "err", err)
			fmt.Fprintf(os.Stderr, "proxy exit firewall: %v\n", err)
			fail = 1
		}
	}

	// 失败回滚（§6「新配置应用失败不得清除旧配置」）：撤掉**本轮刚装上的**
	// 路由/防火墙/接口，让 FIB 回到上一代。三处不在此列，各有理由：
	//   - pinDefaultGateway 不回滚：它把被隧道盖住的默认网关钉回物理口，
	//     回滚 = 重新劫持网关，直接断网
	//   - Proxy client 段不回滚；出口 NAT/ACL 在成功收尾时单独原子提交/局部回退
	//   - ExtraExec 不回滚：逃生舱 argv 不可逆，硬猜逆命令比不回滚更危险
	if fail != 0 && !dry && !applied.empty() {
		cmds := rollbackCmds(snap, &applied)
		log.Warn("apply 失败，回滚本轮已应用的操作（保留上一代配置）",
			"routes", len(applied.routes), "firewall", len(applied.firewall),
			"ifaces", len(applied.ifaces), "rollback_cmds", len(cmds))
		rbFail := 0
		for _, c := range cmds {
			if err := runOp(false, c.argv); err != nil {
				fmt.Fprintf(os.Stderr, "rollback（%s）%v: %v\n", c.note, c.argv, err)
				rbFail = 1
			}
		}
		if rbFail == 0 {
			log.Info("回滚完成，已恢复上一代配置")
		} else {
			log.Error("回滚部分失败，需人工核对 FIB/防火墙")
		}
	}

	if fail == 0 {
		log.Info("apply 完成", "dry", dry)
	} else {
		log.Error("apply 部分失败", "fail", fail, "dry", dry)
	}
	return fail
}

// preferBSDNetTools 当前环境是否该用 route/ifconfig 而不是 ip。
//
// 判据是 system.json 的 capabilities，**不是 runtime.GOOS**：macOS 装了
// iproute2mac 时 `ip` 可用，那条路更统一；没装才是 BSD 语义。Linux 一般有 ip，
// 极老的发行版退到 net-tools 的 route，参数形状与 darwin 的 route 同族。
func preferBSDNetTools(snap *shared.SystemSnapshot) bool {
	if snap == nil {
		// 测试与未知快照：保持既有 `ip route` 形状，避免静默换方言
		return false
	}
	return !snap.Capabilities["ip"]
}

// resolveDevice 把 bundle 里的**逻辑设备名**解析成此刻真实存在的接口。
//
// 情况一（darwin/gnb）：配置写 ifname gnb_tun，但 gnb_drv_darwin.c:249-252
// 在 tun_alloc 之后用 UTUN_OPT_IFNAME 把 ifname 覆盖成 utunN。
// 情况二（windows/gnb + wintun）：适配器名是 **P2PNet**，不是 gnb_tun；
// netsh 的 interface= 吃**名称或 ifIndex**（MicrosoftDocs netsh-interface.md）。
// 情况三：接口还在、名字也对，直接用。
// 解析不出来时**原样返回**，让后面的命令去失败 —— 静默换成别的接口
// 会把路由装到错误的网卡上，比失败更糟。
func resolveDevice(name string, localAddrs []string) string {
	if name == "" {
		return name
	}
	if _, err := net.InterfaceByName(name); err == nil {
		return platformIfaceToken(name)
	}
	// 1) 用本机 overlay IP 反查（darwin utunN / windows P2PNet）
	for _, cidr := range localAddrs {
		ip := parseAddr(cidr)
		if ip == nil {
			continue
		}
		if iface := ifaceWithIP(ip); iface != "" {
			return platformIfaceToken(iface)
		}
	}
	// 2) 平台隧道口兜底（windows wintun P2PNet / ifIndex）
	return platformTunnelIface(name)
}

func parseAddr(s string) net.IP {
	if ip, _, err := net.ParseCIDR(s); err == nil {
		return ip
	}
	return net.ParseIP(s)
}

func ifaceWithIP(ip net.IP) string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ipnet.IP.Equal(ip) {
				return iface.Name
			}
		}
	}
	return ""
}

// isHostDest 目的地是主机路由（/32、/128 或裸 IP）还是网段。
func isHostDest(dest string) bool {
	if _, ipnet, err := net.ParseCIDR(dest); err == nil {
		ones, bits := ipnet.Mask.Size()
		return ones == bits
	}
	return net.ParseIP(dest) != nil
}

// hostDestIP 主机路由在 BSD `route` 下要传裸 IP，不带 /32。
func hostDestIP(dest string) string {
	if ip, _, err := net.ParseCIDR(dest); err == nil {
		return ip.String()
	}
	return dest
}

func routeCmds(snap *shared.SystemSnapshot, r RouteOp) []routeCmd {
	// Windows 优先：它既无 `ip` 也无 `route(8)`，preferBSDNetTools 会误判。
	if snap != nil && snap.OS == "windows" {
		return netshRouteCmds(r)
	}
	if preferBSDNetTools(snap) {
		return bsdRouteCmds(r)
	}
	verb := map[string]string{"add": "add", "replace": "replace", "del": "del"}[r.Action]
	if verb == "" {
		verb = "replace"
	}
	argv := []string{"ip", "route", verb, r.Destination}
	if r.Gateway != "" {
		argv = append(argv, "via", r.Gateway)
	}
	if r.Device != "" {
		argv = append(argv, "dev", r.Device)
	}
	if r.Metric > 0 {
		argv = append(argv, "metric", fmt.Sprintf("%d", r.Metric))
	}
	return []routeCmd{{argv: argv}}
}

// netshRouteCmds Windows 的 netsh 方言。
//
// 语法取自 MicrosoftDocs windowsserverdocs netsh-interface.md（2025-10）：
//
//	netsh interface ipv4 add route    [prefix=]<IPv4>/<len> [interface=]<string> [nexthop=]<IPv4> …
//	netsh interface ipv4 delete route [prefix=]<IPv4>/<len> [interface=]<string> [nexthop=]<IPv4> [store=]…
//
// 说明：
//   - `interface` 是**名称或索引**（resolveDevice 给出的名称）
//   - `nexthop` 仅在前缀非 on-link 时需要（我们经隧道口的对端 /32 与 LAN 都是 on-link）
//   - 用 **具名参数**（prefix= interface=）：位置参数在接口名异常时会报
//     「文件名、目录名或卷标语法不正确」/「找不到元素」（183 实测）
//   - store=active：随重启消失，与 apply --clear 对称，不污染 persistent 存储
func netshRouteCmds(r RouteOp) []routeCmd {
	prefix := r.Destination
	if isHostDest(prefix) {
		if ip := net.ParseIP(hostDestIP(prefix)); ip != nil {
			prefix = ip.String() + "/32"
		}
	}
	verb := map[string]string{"add": "add", "replace": "add", "del": "delete"}[r.Action]
	if verb == "" {
		verb = "add"
	}
	build := func(v string) []string {
		argv := []string{"netsh", "interface", "ipv4", v, "route", "prefix=" + prefix}
		if r.Device != "" {
			// 文档：interface=<名称或索引>
			argv = append(argv, "interface="+r.Device)
		}
		// nexthop 仅非 on-link；我们只走隧道口 on-link，除非显式给了 Gateway
		if r.Gateway != "" {
			argv = append(argv, "nexthop="+r.Gateway)
		}
		if v == "add" && r.Metric > 0 {
			argv = append(argv, fmt.Sprintf("metric=%d", r.Metric))
		}
		argv = append(argv, "store=active")
		return argv
	}
	if r.Action == "del" {
		return []routeCmd{{argv: build("delete")}}
	}
	if r.Action == "add" {
		return []routeCmd{{argv: build("add")}}
	}
	// replace：netsh 无 replace，先 delete（忽略不存在）再 add —— 但只在
	// 所有权闸放行后才会走到这里（见 executeApply）。
	return []routeCmd{
		{argv: build("delete"), optional: true},
		{argv: build("add")},
	}
}

// bsdRouteCmds darwin/net-tools 的 route 方言。
//
// macOS 的 route 没有 replace，只能 delete（忽略「不存在」）+ add。
// metric 在这条方言下没有对应物，直接丢掉而不是拼一个无效参数。
func bsdRouteCmds(r RouteOp) []routeCmd {
	kind := "-net"
	dest := r.Destination
	if isHostDest(dest) {
		kind = "-host"
		dest = hostDestIP(dest)
	}
	build := func(verb string) []string {
		argv := []string{"route", "-n", verb, kind, dest}
		// delete 只要目的地；带上 -interface 在接口已换名（utun 重分配）时
		// 反而删不掉旧路由。
		if verb == "delete" {
			return argv
		}
		if r.Gateway != "" {
			argv = append(argv, r.Gateway)
		} else if r.Device != "" {
			argv = append(argv, "-interface", r.Device)
		}
		return argv
	}
	switch r.Action {
	case "del":
		return []routeCmd{{argv: build("delete")}}
	case "add":
		return []routeCmd{{argv: build("add")}}
	default: // replace
		return []routeCmd{
			{argv: build("delete"), optional: true},
			{argv: build("add")},
		}
	}
}

func firewallArgv(snap *shared.SystemSnapshot, f FirewallOp) []string {
	switch f.Action {
	case "ensure-masq":
		if snap.Capabilities["nft"] {
			return []string{"nft", "add", "rule", "ip", "nat", "POSTROUTING",
				"oifname", f.Device, "masquerade"}
		}
		if snap.Capabilities["iptables"] {
			return []string{"iptables", "-t", "nat", "-A", "POSTROUTING",
				"-o", f.Device, "-j", "MASQUERADE"}
		}
		return nil
	case "open-port":
		// N5：nft 优先（fw4/nftables 主机），iptables 兜底（fw3/legacy）。
		// 原先只写 iptables 路径，纯 nft 主机上 open-port 会静默变成 no-op。
		if snap.Capabilities["nft"] {
			// inet filter INPUT：与 fw4 的 input 链同表，reload 后由 nftables.d 重建。
			argv := []string{"nft", "add", "rule", "ip", "filter", "INPUT",
				"ip", "protocol", f.Proto, f.Proto, "dport", fmt.Sprintf("%d", f.Port), "accept"}
			if f.Comment != "" {
				argv = append(argv, "comment", fmt.Sprintf("%q", f.Comment))
			}
			return argv
		}
		if snap.Capabilities["iptables"] {
			argv := []string{"iptables", "-A", "INPUT", "-p", f.Proto, "--dport", fmt.Sprintf("%d", f.Port), "-j", "ACCEPT"}
			if f.Comment != "" {
				argv = append(argv, "-m", "comment", "--comment", f.Comment)
			}
			return argv
		}
		return nil
	default:
		return nil
	}
}

func ifaceArgv(snap *shared.SystemSnapshot, i IfaceOp) []string {
	verb := map[string]string{"up": "up", "down": "down"}[i.Action]
	if preferBSDNetTools(snap) {
		return []string{"ifconfig", i.Name, verb}
	}
	return []string{"ip", "link", "set", "dev", i.Name, verb}
}

// ifaceInverseAction 只反转我们做过的那一半：up→down、down→up。
func ifaceInverseAction(action string) string {
	switch action {
	case "up":
		return "down"
	case "down":
		return "up"
	default:
		return action
	}
}

// firewallRollbackArgv 按**同一 rule spec** 反删防火墙规则（iptables -D / nft delete rule）。
//
// 与 firewallArgv 严格对称：同一 table/chain/匹配式/动作，只把安装动词换成删除。
// 不是「按注释扫一遍删」——那会误删管理员自己写的同规则；这里删的正是本轮刚追加的那条。
// 只在 rollback（原始 apply 已失败）里调用，因此命令一律 optional：回滚失败要如实报告，
// 但不能盖掉真正的失败原因。
func firewallRollbackArgv(snap *shared.SystemSnapshot, f FirewallOp) []string {
	switch f.Action {
	case "ensure-masq":
		if snap.Capabilities["nft"] {
			return []string{"nft", "delete", "rule", "ip", "nat", "POSTROUTING",
				"oifname", f.Device, "masquerade"}
		}
		if snap.Capabilities["iptables"] {
			return []string{"iptables", "-t", "nat", "-D", "POSTROUTING",
				"-o", f.Device, "-j", "MASQUERADE"}
		}
		return nil
	case "open-port":
		if snap.Capabilities["nft"] {
			argv := []string{"nft", "delete", "rule", "ip", "filter", "INPUT",
				"ip", "protocol", f.Proto, f.Proto, "dport", fmt.Sprintf("%d", f.Port), "accept"}
			if f.Comment != "" {
				argv = append(argv, "comment", fmt.Sprintf("%q", f.Comment))
			}
			return argv
		}
		if snap.Capabilities["iptables"] {
			argv := []string{"iptables", "-D", "INPUT", "-p", f.Proto, "--dport", fmt.Sprintf("%d", f.Port), "-j", "ACCEPT"}
			if f.Comment != "" {
				argv = append(argv, "-m", "comment", "--comment", f.Comment)
			}
			return argv
		}
		return nil
	default:
		return nil
	}
}

// rollbackCmds 由已应用的操作推出逆操作命令（后装的先撤）。
//
// 路由只反转 add/replace → del：del 是「按所有权闸删掉的自己的路由」，
// 其逆（重装）不在计划里也不该猜 —— 旧一代若要恢复，由「保留上一代配置」
// 那条路径负责，不由本函数负责重建。
func rollbackCmds(snap *shared.SystemSnapshot, j *applyJournal) []routeCmd {
	out := make([]routeCmd, 0, len(j.routes)+len(j.firewall)+len(j.ifaces))
	for i := len(j.routes) - 1; i >= 0; i-- {
		r := j.routes[i]
		if r.Action == "del" {
			continue
		}
		inv := RouteOp{Destination: r.Destination, Gateway: r.Gateway, Device: r.Device, Action: "del"}
		for _, c := range routeCmds(snap, inv) {
			c.optional = true
			c.note = "rollback route"
			out = append(out, c)
		}
	}
	for i := len(j.firewall) - 1; i >= 0; i-- {
		if argv := firewallRollbackArgv(snap, j.firewall[i]); len(argv) != 0 {
			out = append(out, routeCmd{argv: argv, optional: true, note: "rollback firewall"})
		}
	}
	for i := len(j.ifaces) - 1; i >= 0; i-- {
		op := j.ifaces[i]
		inv := IfaceOp{Name: op.Name, Action: ifaceInverseAction(op.Action), MTU: op.MTU}
		out = append(out, routeCmd{argv: ifaceArgv(snap, inv), optional: true, note: "rollback iface"})
	}
	return out
}

// resolveBin 把命令名解析成可执行路径。
//
// ⚠️ daemon 环境的 PATH 往往是残的：systemd 的默认 PATH 不含 /sbin，
// 我们手工起 mynetd 时也容易漏掉 /sbin —— 而 `route`/`ifconfig` 就在
// /sbin 里。症状是 apply 在终端能跑、在向导里报
// `exec: "route": executable file not found in $PATH`。
// 所以 LookPath 失败后再扫几个系统目录，而不是直接丢给 exec。
func resolveBin(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/sbin", "/usr/sbin", "/usr/bin", "/bin"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return name
}

func runOp(dry bool, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty argv")
	}
	if dry {
		fmt.Printf("DRY-RUN: %v\n", argv)
		return nil
	}
	argv = append([]string{resolveBin(argv[0])}, argv[1:]...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// localConnectedNets 本机物理口的直连 IPv4 网段（跳过 lo / 隧道口）。
//
// 隧道口（utun/tun/gnb/wg）不算：它们是我们自己装路由的目标，
// 用它们的网段去过滤会把自己的 overlay 路由也滤掉。
func localConnectedNets() []*net.IPNet {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var nets []*net.IPNet
	for _, iface := range ifaces {
		name := iface.Name
		if strings.HasPrefix(name, "lo") ||
			strings.HasPrefix(name, "utun") ||
			strings.HasPrefix(name, "tun") ||
			strings.HasPrefix(name, "gnb") ||
			strings.HasPrefix(name, "wg") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			nets = append(nets, ipnet)
		}
	}
	return nets
}

// parseDestNet 目的地解析成 IPv4 网段；裸 IP 当 /32。
func parseDestNet(dest string) *net.IPNet {
	if dest == "" {
		return nil
	}
	if _, ipnet, err := net.ParseCIDR(dest); err == nil {
		if ipnet.IP.To4() == nil {
			return nil
		}
		return ipnet
	}
	ip := net.ParseIP(dest)
	if ip == nil || ip.To4() == nil {
		return nil
	}
	return &net.IPNet{IP: ip.To4(), Mask: net.CIDRMask(32, 32)}
}

// overlapsAny dest 与任一网段互相包含即冲突。
// 同网段（192.168.0.0/24 vs 本机 192.168.0.0/24）是最常见形态；
// 更宽的 /16 盖住本机 /24 同样劫持。
func overlapsAny(dest *net.IPNet, nets []*net.IPNet) bool {
	if dest == nil {
		return false
	}
	for _, n := range nets {
		if n == nil {
			continue
		}
		if n.Contains(dest.IP) || dest.Contains(n.IP) {
			return true
		}
	}
	return false
}

// pinDefaultGateway 网关被隧道 /24 盖住时，把它钉回物理口 /32。
//
// 对齐 openwrt route_policy.sh:146 的 fix_wan_gateway_route：
// **先查再钉** —— 网关本来就在直连口上时动它，反而会写出错误的
// 邻接项（实测 `add -host gw -interface en0` 把 MAC 写成本机网卡，
// ping 网关 100% 丢包、整机断外网）。已与本机直连网段冲突的路由
// 在装之前就被 filter 跳过了，这里只是残留覆盖的兜底。
func pinDefaultGateway(snap *shared.SystemSnapshot, dry bool) error {
	gw, dev := defaultGateway()
	if gw == "" || dev == "" {
		return nil
	}
	if !gatewayHijacked(gw) {
		return nil
	}
	// BSD：via <gw> 而不是 -interface —— 前者走正常 ARP，后者会钉错 MAC。
	r := RouteOp{Destination: gw + "/32", Gateway: gw, Device: dev, Action: "replace"}
	if preferBSDNetTools(snap) {
		// host 路由去掉 /32 后缀；gateway 保留（见上）
		r.Destination = gw
	}
	for _, c := range routeCmds(snap, r) {
		if err := runOp(dry, c.argv); err != nil {
			if c.optional {
				continue
			}
			return err
		}
	}
	return nil
}

// gatewayHijacked 默认网关当前是否被路由进隧道口。
//
// 这里要的是**转发路径**（最长前缀 + 默认路由兜底），不是所有权：网关一般
// 没有自己的 /32，只有覆盖路由能暴露它被隧道盖住了。
func gatewayHijacked(gw string) bool {
	dev := routeLookupDev(gw)
	if dev == "" {
		return false
	}
	return strings.HasPrefix(dev, "utun") || strings.HasPrefix(dev, "tun") ||
		strings.HasPrefix(dev, "gnb") || strings.HasPrefix(dev, "wg")
}

// destOwnedBy 目的地是否允许我们动：
//
//   - 空闲（查不到现路由）→ 可以装
//   - 已挂在**我们的**隧道口 → 是我们装的，可以 replace/del
//   - 挂在别人（直连口、别的隧道、静态路由）→ 不碰
//
// 「只删自己配置的」的落点。对齐 openwrt node.lua:clear_routes
// （`ip route show dev $tun` 再删，绝不按目的地盲删）。
func destOwnedBy(dest, ourDev string) bool {
	return routeTouched(destOwnerDev(dest), ourDev)
}

// routeTouched 纯策略：owner 是目的地现挂设备（"" = 空闲），
// ourDev 是本次操作的目标隧道口。只有空闲或自己装的才准动。
func routeTouched(owner, ourDev string) bool {
	if owner == "" {
		return true
	}
	return ourDev != "" && owner == ourDev
}

// clearTunnelRoutes 清掉隧道口上的路由（`mynet apply --clear`）。
//
// 对齐 openwrt node.lua:clear_routes：`ip route show dev <tun>` 后逐条 del，
// **只动本隧道设备上的**，绝不按目的地删 —— 别的口的静态路由碰都不碰。
// stop 服务 / 换代时调用。
func clearTunnelRoutes(snap *shared.SystemSnapshot, dry bool) int {
	devs := tunnelDevices()
	if len(devs) == 0 {
		fmt.Println("apply --clear: 未发现隧道口，无路由可清")
		return 0
	}
	fail := 0
	for _, dev := range devs {
		dests := routesOnDevice(dev)
		if len(dests) == 0 {
			continue
		}
		fmt.Printf("clear %s: %d 条\n", dev, len(dests))
		for _, dest := range dests {
			r := RouteOp{Destination: dest, Device: dev, Action: "del"}
			for _, c := range routeCmds(snap, r) {
				if err := runOp(dry, c.argv); err != nil {
					if c.optional {
						continue
					}
					fmt.Fprintf(os.Stderr, "clear %s %s: %v\n", dev, dest, err)
					fail = 1
				}
			}
		}
	}
	return fail
}

// tunnelDevices 本机隧道接口名（gnb/wg/utun/tun 前缀）。
func tunnelDevices() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		name := iface.Name
		if strings.HasPrefix(name, "utun") ||
			strings.HasPrefix(name, "tun") ||
			strings.HasPrefix(name, "gnb") ||
			strings.HasPrefix(name, "wg") {
			out = append(out, name)
		}
	}
	return out
}

// normalizeBSDDest netstat 的 192.168.8 这种缩写 → 192.168.8.0/24。
func normalizeBSDDest(d string) string {
	if strings.Contains(d, "/") {
		return d
	}
	parts := strings.Split(d, ".")
	switch len(parts) {
	case 1:
		return d + ".0.0.0/8"
	case 2:
		return d + ".0.0/16"
	case 3:
		return d + ".0/24"
	default:
		if net.ParseIP(d) != nil {
			return d + "/32"
		}
		return d
	}
}

// firewall 批量写入仍由 L6 的唯一执行入口持有，stdin 不经 shell 插值。
func runProxyFirewall(dry bool, argv []string, input string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty firewall argv")
	}
	if dry {
		fmt.Printf("DRY-RUN firewall: %v\n%s", argv, input)
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, resolveBin(argv[0]), argv[1:]...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w: %s", argv[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
