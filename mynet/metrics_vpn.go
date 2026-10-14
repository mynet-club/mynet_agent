package mynet

import (
	"net"
	"os/exec"
	"strings"

	"github.com/mynet-club/mynet_agent/mynet/driver"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

// probeWireGuard / probeGNB 都**调 driver 包**，不再自己解析。
//
// 早先这里各写了一份半成品：解析循环体就是 `_ = fields` / `_ = iface`，
// 最后只返回一个 `Up: true` 的空壳 —— 也就是说 `Interface` / `Peers` /
// `PeersUp` / `Addrs` 四个字段**从来没被赋过值**，`mynet metrics` 报出来的
// VPN 段永远是 `{"driver":"wireguard","up":true}`。
//
// 而 driver 包早就有完整实现（`wireguard.go` 的 `parseWGDump` 解析
// handshake / allowedIPs / endpoint / rx / tx，且有 `TestParseWGDump` 覆盖）。
// 两处各写一份解析必然漂移，所以这里只保留「调 driver + 投影成统一视图」。
func probeWireGuard() []VPNDriverMetrics {
	iface := detectWGInterface()
	if iface == "" {
		return downProbeFallback(domain.DriverTypeWireGuard)
	}
	d := driver.NewWireGuardDriver(iface, 0)
	peers, err := d.GetAllPeers()
	if err != nil {
		// 接口在、但读不到 peer（非 root 时 `wg show <iface> dump` 会
		// Permission denied，本机实测就是这样）。
		//
		// ⚠️ 这种情况下**仍然要报「接口存在」**，不能返回 nil：后端的
		// `isVpnInterfaceDown()` 把「心跳里没有 vpn_interfaces 段」直接判成
		// 「网络不可用」（NodeHeartbeatController.php:421-432），于是走
		// markNodeOffline 分支、连 node_monitor 都不处理。
		// 也就是说「读不到 peer 数」会被上报成「节点掉线」——
		// 少报一个数字只是数据不全，误报离线是**状态错误**，后者严重得多
		// （运维会看到一片节点离线，而它们其实好好的）。
		return []VPNDriverMetrics{{Driver: "wireguard", Interface: iface, Up: true}}
	}
	return []VPNDriverMetrics{projectPeers("wireguard", iface, peers)}
}

// detectWGInterface 用 `wg show interfaces` 找活动接口，**不写死 wg0** ——
// macOS 上是 utunN，Linux 上也可能不是 wg0。
// 这条命令不需要 root（`wg show <iface> dump` 才需要），所以探测本身在普通
// 权限下可用；拿不到 dump 时由调用方降级。
func detectWGInterface() string {
	out, err := exec.Command("wg", "show", "interfaces").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// probeGNB 探测 gnb。
//
// ⚠️ 与 probeWireGuard 同一条红线：**只要接口在就必须报出来**，不能返回 nil。
// 后端的 isVpnInterfaceDown() 把「心跳里没有 vpn_interfaces 段」直接判成
// 「网络不可用」→ markNodeOffline → 连 node_monitor 都不处理
// （NodeHeartbeatController.php:88-108）。少报 peer 数只是数据不全，
// 误报离线是**状态错误**。
//
// 此前这里把这条红线踩了两遍（wg 侧的注释早就写下了这个坑，gnb 侧没跟上）：
//  1. 接口名硬编码成 ""（`projectPeers("gnb", "", peers)`）。而 ifname 是
//     后端的 required_with:vpn_interfaces，空值会让整条心跳 422 ——
//     于是 heartbeat.go 干脆整段不发，还写着「后端的 isVpnInterfaceDown(null)
//     不会因此判节点下线」，**那句是错的**：源码第一行就是 empty() → return true。
//     实测后果：隧道 Direct Point to Point、ping 0% 丢包，而后端侧
//     Redis status=offline、node_monitor_snapshots 0 行、nodes.last_seen NULL。
//  2. 用 d.IsAvailable() 当闸门，而它当时是 `gnb_ctl -s`（不带 -b）——
//     gnb_ctl 缺 -b 时只打印 usage 就 exit 0，于是「可用」恒为真，
//     GetAllPeers 再把那段 usage 当状态解析出 0 个 peer。
func probeGNB() []VPNDriverMetrics {
	ifname, addrs := detectGNBInterface()
	if ifname == "" {
		return downProbeFallback(domain.DriverTypeGNB)
	}
	d := driver.NewGNBDriver(0)
	d.ConfDir = gnbConfDir()
	peers, err := d.GetAllPeers()
	if err != nil {
		// gnb.map 是 0600 root:root（gnb 以 root 跑），而 mynetd 是非 root
		// （它启动时自己就打印「mynetd 无需 root」）—— 实测读到
		// `open ctl block error`。读不到 peer 数**不影响接口在**这个事实，
		// 照样上报，理由同 probeWireGuard。
		return []VPNDriverMetrics{{Driver: "gnb", Interface: ifname, Up: true, Addrs: addrs}}
	}
	m := projectPeers("gnb", ifname, peers)
	if len(m.Addrs) == 0 {
		m.Addrs = addrs // peer 的 tun 地址之外，本机地址从接口上取
	}

	return []VPNDriverMetrics{m}
}

// downProbeFallback tun 未起时的「down 格」探测（缺口⑥）：
// 期望口名（bundle 的 node.interface 权威）在、接口没起 —— 报 Up:false 的一格，
// 让心跳把「服务 active 但 tun 未起」的信号带上去（vpn_interfaces.*.status=down
// 是后端契约既有值，此前从不发；后端对 down 行与缺段同判离线，行为不变，
// 差别是信号可见：期望口名在场，不再是「什么都没有」）。
//
// 只在 bundle 的**主驱动**对得上时报：期望口名按驱动各自归属，
// gnb 节点的口名不能记到 wireguard 头上。期望口名拿不到就不报（不猜）。
func downProbeFallback(drv domain.VPNDriverType) []VPNDriverMetrics {
	name, bundleDrv, ok := tunNameFromBundle()
	if !ok || bundleDrv != drv {
		return nil
	}
	return []VPNDriverMetrics{{Driver: string(drv), Interface: name, Up: false}}
}

// countUpVPNIfaces 显示面口径的 VPN 口计数：只数 up 的活口。
// 探测面会报 tun 未起的 down 格（缺口⑥），`vpn=N` 这类摘要若按条数数，
// 一个没起来的口会被数成「1 个 VPN」——语义就变了。
func countUpVPNIfaces(ms []VPNDriverMetrics) int {
	n := 0
	for _, m := range ms {
		if m.Up {
			n++
		}
	}
	return n
}

// detectGNBInterface 取 gnb 的接口名，并用标准库确认它真的存在且 UP。
//
// 为什么从 bundle 取而不是问 gnb_ctl：`gnb_ctl -b … -c -s` 的输出里
// **没有接口名**（只有 conf_dir / node_num / wan*_port 与每个 node 的 tun_ipv4），
// 而读 ctl block 又要 root。bundle 的 node.interface 是后端侧唯一权威
// （= node_configs.config.ifname，见 domain.BundleEnvelope.TunDevice），
// 也正是我们渲染进 node.conf 的那个值，读它不需要任何特权。
//
// 存在性与 UP 用 net.InterfaceByName 判 —— 标准库、跨平台、不 exec、不要 root
// （对照 detectWGInterface 要 shell 出 wg）。
func detectGNBInterface() (string, []string) {
	ifname := gnbIfnameFromBundle()
	if ifname == "" {
		return "", nil
	}
	iface, err := net.InterfaceByName(ifname)
	if err != nil || iface.Flags&net.FlagUp == 0 {
		return "", nil
	}
	var addrs []string
	if as, err := iface.Addrs(); err == nil {
		for _, a := range as {
			// 只要 IPv4：后端的 vpn_interfaces.*.ip 是单个字符串，
			// 而 gnb 的 tun 会同时挂 64:ff9b:: 前缀的 v6 与 fe80:: 链路本地
			if s := a.String(); !strings.Contains(s, ":") {
				addrs = append(addrs, s)
			}
		}
	}

	return ifname, addrs
}

// gnbIfnameFromBundle 从 $MYNET_HOME/var/bundle.json 取 gnb 的接口名。
//
// 与投影/路由同一解析（env.TunDevice，权威=node.interface），**不再优先读
// gnb.options.ifname**：三处读两个字段的话，后端一旦只发权威字段，这里就会报
// 「没有接口」而 apply 那边照着新名字装 —— 展示与事实分叉比展示缺失更坏。
// 解析失败时返回空（宁可这一项不报，也不报一个猜出来的名字）。
func gnbIfnameFromBundle() string {
	env, err := loadBundleEnvelope()
	if err != nil || env.Gnb == nil {
		return ""
	}
	name, derr := env.TunDevice()
	if derr != nil {
		return ""
	}

	return name
}

// projectPeers 把 driver 的 peer 列表投影成统一视图。
//
// 字段名刻意不含驱动原生术语（B4 红线：业务对象不得含驱动术语）——
// 这里是 Peers / PeersUp / Addrs，不是 AllowedIPs / LatestHandshake。
// `driver=` 用于标识来源，是允许的（与 Envelope 一致）。
func projectPeers(drv, iface string, peers []*domain.VPNPeer) VPNDriverMetrics {
	m := VPNDriverMetrics{Driver: drv, Interface: iface, Up: true}
	seen := make(map[string]bool, len(peers)*2)
	for _, p := range peers {
		if p == nil || p.IsLocal {
			continue // 本机自己不算 peer，否则 Peers 恒多 1
		}
		m.Peers++
		if p.IsOnline() {
			m.PeersUp++
		}
		m.RxBytes += p.RxBytes
		m.TxBytes += p.TxBytes
		for _, a := range []string{p.VirtualIP, p.VirtualIP6} {
			if a != "" && !seen[a] {
				seen[a] = true
				m.Addrs = append(m.Addrs, a)
			}
		}
	}
	return m
}
