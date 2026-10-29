package mynet

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mynet-club/mynet_agent/mynet/driver"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// 本机节点状态机（P1）—— 判据全部来自**磁盘产物 + 进程 + 内核**，
// 不靠内存计数器（CPI-68：落盘结果比命令退出码可信）。
//
// 状态一经算定，前端只渲染：横幅一言之、主按钮、按钮禁用表，
// 都由这里给出。前端不许自己拼状态机 —— 那会和 CLI 判据漂移。

// NodeState 本机节点生命周期状态。
type NodeState string

const (
	StateUninstalled NodeState = "UNINSTALLED"
	StateInstalled   NodeState = "INSTALLED"
	StateConfigured  NodeState = "CONFIGURED"
	StateRunning     NodeState = "RUNNING"
	StateDegraded    NodeState = "DEGRADED"
	StateFailed      NodeState = "FAILED"
	StateStopped     NodeState = "STOPPED"
)

// NodeStatus 状态机输出。one_line 是「状态一言之」；actions 是按钮面。
type NodeStatus struct {
	State   NodeState `json:"state"`
	OneLine string    `json:"one_line"`
	// PrimaryAction 主按钮动作 id；空表示无（不应发生）。
	PrimaryAction string `json:"primary_action"`
	// Actions 全部动作的可用性。键是动作 id，值是是否可点。
	// 前端按这张表禁用/启用，不要自己再判一遍状态。
	Actions map[string]bool `json:"actions"`
	// 按钮文案（前端直接用，保证「一言之」与按钮用词一致）
	Labels map[string]string `json:"labels"`
	// 明细区
	Home           string   `json:"home"`
	GnbBin         string   `json:"gnb_bin"`
	Installed      bool     `json:"installed"`
	Configured     bool     `json:"configured"`
	ProcessUp      bool     `json:"process_up"`
	ProcessPID     int      `json:"process_pid,omitempty"`
	TunIface       string   `json:"tun_iface,omitempty"`
	OverlayIP      string   `json:"overlay_ip,omitempty"`
	TunUp          bool     `json:"tun_up"`
	RoutesOK       bool     `json:"routes_ok"`
	NodeID         string   `json:"node_id,omitempty"`
	ActiveNodeID   string   `json:"active_node_id,omitempty"`
	ConfigMismatch bool     `json:"config_mismatch"`
	PeersTotal     int      `json:"peers_total"`
	PeersRouted    int      `json:"peers_routed"`
	MissingRoutes  []string `json:"missing_routes,omitempty"`
	// Detail 一言之外的补充说明（可空），比如「缺 2 条路由」
	Detail string `json:"detail,omitempty"`

	// Tunnel 隧道健康信号（缺口⑥）：tun 是否存在、挂没挂 VPN IP。
	// 与 TunUp 的区别：TunUp 只回答「按 IP 反查有没有接口」，这里把
	// 期望口名/VPN IP/两态事实分列，供状态面与启动验收共用判据。
	Tunnel TunHealth `json:"tunnel_health"`

	// ── ④ 运行区（P2）──────────────────────────────────────
	UptimeSec int64 `json:"uptime_sec,omitempty"`
	Mtu       int   `json:"mtu,omitempty"`
	RxBytes   int64 `json:"rx_bytes,omitempty"`
	TxBytes   int64 `json:"tx_bytes,omitempty"`

	// ── ⑤ 对端与路由区（P2）────────────────────────────────
	// Peers 含本机那一行 + 每个对端一行。每行三件事：
	// 配置里有没有、OS 路由装没装、链路通不通（gnb_ctl 有则给）。
	Peers []PeerRow `json:"peers"`
	// RouteConfText route.conf 原文（本机在前、节点一律 /32），供「配置」折叠区
	RouteConfText string `json:"route_conf_text,omitempty"`
	// SystemRoutesText 系统路由表摘录（netstat/route），供「系统路由」标签
	SystemRoutesText string `json:"system_routes_text,omitempty"`
}

// PeerRow 对端表一行。Routed 是内核路由是否装上 —— 这是 DEGRADED 的判据。
type PeerRow struct {
	NodeID  string `json:"node_id"`
	Overlay string `json:"overlay,omitempty"`
	IsSelf  bool   `json:"is_self"`
	Routed  bool   `json:"routed"`
	// Link 链路态：Direct / Relay / unknown（gnb_ctl 不可用时）
	Link string `json:"link,omitempty"`
	// LatencyMs gnb_ctl 报的时延；0 表示未知
	LatencyMs int `json:"latency_ms,omitempty"`
}

// 动作 id —— 前后端共用的契约
const (
	ActInstall   = "install"
	ActConfigure = "configure"
	ActStart     = "start"
	ActRestart   = "restart"
	ActStop      = "stop"
	ActFixRoutes = "fix-routes"
	ActUninstall = "uninstall"
	ActRetry     = "retry"
)

// statusProbe 探测面。默认实现读真环境；测试注入替身。
type statusProbe struct {
	home string
	// gnbPath gnb 可执行文件；空表示未安装
	gnbPath string
	// pidAlive 真实 pid 是否存活
	pidAlive func(pid int) bool
	// ifaceStats 接口计数器（RX/TX/MTU）；接口不存在时返回 ok=false
	ifaceStats func(iface string) (rx, tx int64, mtu int, ok bool)
	// procUptimeSec 进程运行时长（秒）；未知返回 0
	procUptimeSec func(pid int) int64
	// routeExists 查一条 OS 路由是否已装（目的地 CIDR 或 IP）
	routeExists func(dest string) bool
	// localRouteExists 目的地已由本机物理接口承载；这类网段不应计为 MyNet 缺失。
	localRouteExists func(dest string) bool
	// findIface 找挂了某 IP 的接口名
	findIface func(ip string) string
	// ifaceExists 按接口名查存在（tun 健康信号的「tun 在不在」事实；可空）
	ifaceExists func(name string) bool
	// gnbPeerLinks 读 gnb_ctl 的对端链路态；不可用时返回 nil
	// （前端显示 unknown，不猜）。key = nodeID。
	gnbPeerLinks func(confDir string) map[int64]PeerLinkInfo
}

// ComputeNodeStatus 算一次本机节点状态。
func ComputeNodeStatus() NodeStatus {
	return computeNodeStatus(defaultProbe())
}

func defaultProbe() statusProbe {
	home := utils.GetMyNetHome()
	return statusProbe{
		home:     home,
		gnbPath:  lookGnb(),
		pidAlive: pidAlive,
		routeExists: func(dest string) bool {
			return osRouteExists(dest)
		},
		localRouteExists: localRouteExists,
		findIface:        ifaceWithAddr,
		ifaceExists:      ifaceExistsByName,
		ifaceStats:       ifaceStats,
		procUptimeSec:    procUptimeSec,
		gnbPeerLinks:     gnbPeerLinksOf,
	}
}

// PeerLinkInfo 本地网界面的对端链路态（脱敏后的最小子集）。
type PeerLinkInfo struct {
	Link      string
	Overlay   string
	LatencyMs float64
}

// gnbPeerLinksOf 委托 driver 包读 gnb_ctl。失败返回 nil ——
// 链路态拿不到就是拿不到，不编。
func gnbPeerLinksOf(confDir string) map[int64]PeerLinkInfo {
	raw := driver.GnbPeerLinks(confDir)
	if raw == nil {
		return nil
	}
	m := make(map[int64]PeerLinkInfo, len(raw))
	for id, p := range raw {
		m[id] = PeerLinkInfo{Link: p.Link, Overlay: p.Overlay, LatencyMs: p.LatencyMs}
	}
	return m
}

// ifaceStats 读接口计数器。Linux 走 sysfs，darwin 用 netstat -ibn（utun 的
// ifconfig 不报 RX/TX bytes，会恒 0 —— 2026-10-28 本机 utun4 实测），
// Windows 用 Get-NetAdapterStatistics。
func ifaceStats(iface string) (rx, tx int64, mtu int, ok bool) {
	// Linux：/sys/class/net/<if>/statistics/{rx,tx}_bytes
	if b, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "statistics", "rx_bytes")); err == nil {
		rx, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if b, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "statistics", "tx_bytes")); err == nil {
			tx, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		}
		if b, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "mtu")); err == nil {
			mtu, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		return rx, tx, mtu, true
	}
	// Windows：Get-NetAdapterStatistics（node_status_iface_windows.go）
	if rx, tx, mtu, ok := windowsIfaceBytes(iface); ok {
		return rx, tx, mtu, true
	}
	// darwin/BSD：netstat -ibn 的 <Link#> 行才有 Ibytes/Obytes
	if rx, tx, mtu, ok := netstatIfaceBytes(iface); ok {
		return rx, tx, mtu, true
	}
	// 回退：ifconfig 只保证 mtu（utun 常不报计数器）
	bin := lookPathAny("ifconfig")
	if bin == "" {
		return 0, 0, 0, false
	}
	out, err := exec.Command(bin, iface).Output()
	if err != nil {
		return 0, 0, 0, false
	}
	s := string(out)
	if !strings.Contains(s, iface+":") && !strings.Contains(s, iface+" ") {
		return 0, 0, 0, false
	}
	for _, ln := range strings.Split(s, "\n") {
		f := strings.Fields(ln)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "mtu" {
				mtu, _ = strconv.Atoi(f[i+1])
			}
		}
		if n := parseCountAfter(ln, "bytes"); n > 0 && strings.Contains(ln, "RX") {
			rx = n
		}
		if n := parseCountAfter(ln, "bytes"); n > 0 && strings.Contains(ln, "TX") {
			tx = n
		}
	}
	return rx, tx, mtu, true
}

// netstatIfaceBytes 解析 `netstat -ibn` 中接口的 <Link#> 行。
// 形如：utun4  1400  <Link#25>  597606  0  571673514  318261  0  98392561  0
// 列：Name Mtu Network Ipkts Ierrs Ibytes Opkts Oerrs Obytes Coll（Address 空）
func netstatIfaceBytes(iface string) (rx, tx int64, mtu int, ok bool) {
	bin := lookPathAny("netstat")
	if bin == "" {
		return 0, 0, 0, false
	}
	out, err := exec.Command(bin, "-ibn").Output()
	if err != nil {
		return 0, 0, 0, false
	}
	return parseNetstatIfaceBytes(string(out), iface)
}

// parseNetstatIfaceBytes 从 netstat -ibn 文本取 <Link> 行计数（可测）。
func parseNetstatIfaceBytes(out, iface string) (rx, tx int64, mtu int, ok bool) {
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) < 9 || f[0] != iface || !strings.HasPrefix(f[2], "<Link") {
			continue
		}
		mtu, _ = strconv.Atoi(f[1])
		// <Link> 行无 Address：Ipkts Ierrs Ibytes Opkts Oerrs Obytes = f[3..8]
		rx, _ = strconv.ParseInt(f[5], 10, 64)
		tx, _ = strconv.ParseInt(f[8], 10, 64)
		return rx, tx, mtu, true
	}
	return 0, 0, 0, false
}

func parseCountAfter(s, key string) int64 {
	i := strings.Index(s, key)
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+len(key):])
	if len(f) == 0 {
		return 0
	}
	n, _ := strconv.ParseInt(strings.Trim(f[0], ","), 10, 64)
	return n
}

// procUptimeSec 进程运行时长。Linux 读 /proc/<pid>/stat 的 starttime，
// darwin 用 ps -o etime=（elapsed）。
func procUptimeSec(pid int) int64 {
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		// /proc/<pid>/stat 第 22 个字段是 starttime（clock ticks）
		st, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err == nil {
			// comm 可能带空格括号，从最后一个 ')' 之后切
			s := string(st)
			if i := strings.LastIndex(s, ")"); i >= 0 {
				f := strings.Fields(s[i+1:])
				// f[0]=state … starttime 是 f[19]（0-based，从 state 起）
				if len(f) > 19 {
					startTicks, _ := strconv.ParseInt(f[19], 10, 64)
					var up float64
					fmtS := strings.Fields(string(b))
					if len(fmtS) > 0 {
						up, _ = strconv.ParseFloat(fmtS[0], 64)
					}
					hz := float64(100) // Linux USER_HZ 通常 100
					if up > 0 && startTicks > 0 {
						return int64(up - float64(startTicks)/hz)
					}
				}
			}
		}
	}
	// darwin / 兜底：ps -o etime=
	bin := lookPathAny("ps")
	if bin == "" {
		return 0
	}
	out, err := exec.Command(bin, "-o", "etime=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	return parseETime(strings.TrimSpace(string(out)))
}

// parseETime 解析 ps etime：[[dd-]hh:]mm:ss
func parseETime(s string) int64 {
	if s == "" {
		return 0
	}
	s = strings.TrimSpace(s)
	days := int64(0)
	if i := strings.Index(s, "-"); i >= 0 {
		days, _ = strconv.ParseInt(s[:i], 10, 64)
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	var sec int64
	mult := int64(1)
	for i := len(parts) - 1; i >= 0; i-- {
		n, _ := strconv.ParseInt(parts[i], 10, 64)
		sec += n * mult
		mult *= 60
	}
	return days*86400 + sec
}

func lookGnb() string {
	// 平台可执行名见 gnb_bin_{unix,windows}.go
	name := gnbExeName()
	// 与 driver.resolveGNBBinary / plugin findGnbBin 同一优先级：
	// 标准位是 <prefix>/bin（D4-E：driver/ 安装层退役），旧 home 树只作回退。
	if v := os.Getenv("MYNET_GNB_BIN"); v != "" {
		if st, err := os.Stat(v); err == nil && !st.IsDir() {
			return v
		}
	}
	home := utils.GetMyNetHome()
	dirs := append([]string{
		utils.PrefixBinDir(),
		filepath.Join(home, "driver", "gnb", "bin"), // 旧标准位（未迁移）
		filepath.Join(home, "bin"),
	}, gnbExtraDirs()...)
	for _, dir := range dirs {
		for _, n := range []string{name, "gnb"} {
			p := filepath.Join(dir, n)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// ifaceWithAddr 找挂了该 IPv4 的接口名（darwin 的 utunN / linux 的 gnb_tun）。
func ifaceWithAddr(ip string) string {
	want := net.ParseIP(ip)
	if want == nil {
		return ""
	}
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
			if ipnet.IP.Equal(want) {
				return iface.Name
			}
		}
	}
	return ""
}

// osRouteExists / localRouteExists 见 route_query_{unix,windows}.go。
// darwin 用 `route -n get`，linux 用 `ip route show`，windows 用 `route print`。

// lookPathAny 先走 PATH，失败后扫系统目录。
// mynetd 起来时 PATH 常是残的（/sbin 不在里面），而 route/ifconfig 在 /sbin。
func lookPathAny(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/sbin", "/usr/sbin", "/usr/bin", "/bin"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// readModeFile 读该 home 下的 mode.json：2026-10-12 起在 `conf/` 下，旧位置只读兼容。
func readModeFile(home string) ([]byte, error) {
	for _, p := range []string{
		filepath.Join(home, "conf", "mode.json"),
		filepath.Join(home, "mode.json"),
	} {
		if b, err := os.ReadFile(p); err == nil {
			return b, nil
		}
	}
	return nil, os.ErrNotExist
}

// nodeDirHasKeys 节点唯一存放位里有没有身份材料（D4-E）：
// gnb=security/<id>.private、wg=wg.conf（PrivateKey 在内）。
func nodeDirHasKeys(home, form, nodeID string) bool {
	if nodeID == "" {
		return false
	}
	dir := filepath.Join(home, "conf", form, nodeID)
	return fileNonEmpty(filepath.Join(dir, "security", nodeID+".private")) ||
		fileNonEmpty(filepath.Join(dir, "wg.conf"))
}

// gnbProjectionDir 与 mynet 的 gnbConfDir 同一口径（D4-E）：
// 节点唯一存放位 conf/<form>/<nodeId>/；旧 driver/gnb/conf 与 var/gnb 只作回退。
func gnbProjectionDir(home string) string {
	nodeID, form := "", "local"
	if raw, err := readModeFile(home); err == nil {
		var m struct {
			Mode   string `json:"mode"`
			Active struct {
				NodeID string `json:"node_id"`
			} `json:"active"`
		}
		if json.Unmarshal(raw, &m) == nil {
			nodeID = m.Active.NodeID
			if m.Mode == "online" {
				form = "online"
			}
		}
	}
	if nodeID != "" {
		p := filepath.Join(home, "conf", form, nodeID)
		if _, err := os.Stat(filepath.Join(p, "node.conf")); err == nil {
			return p
		}
	}
	// 旧投影树（未迁移节点）
	base := filepath.Join(home, "driver", "gnb", "conf")
	if nodeID != "" {
		p := filepath.Join(base, nodeID)
		if _, err := os.Stat(filepath.Join(p, "node.conf")); err == nil {
			return p
		}
	}
	if ents, err := os.ReadDir(base); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				return filepath.Join(base, e.Name())
			}
		}
	}
	// 更旧布局
	return filepath.Join(home, "var", "gnb")
}

func computeNodeStatus(p statusProbe) NodeStatus {
	home := p.home
	conf := filepath.Join(home, "conf")
	gnbConf := gnbProjectionDir(home)
	bundlePath := filepath.Join(home, "var", "bundle.json")

	st := NodeStatus{
		Home: home,
		Actions: map[string]bool{
			ActInstall: false, ActConfigure: false, ActStart: false,
			ActRestart: false, ActStop: false, ActFixRoutes: false,
			ActUninstall: false, ActRetry: false,
		},
		Labels: map[string]string{
			ActInstall:   "安装",
			ActConfigure: "去配置",
			ActStart:     "启动隧道",
			ActRestart:   "重启隧道",
			ActStop:      "停止",
			ActFixRoutes: "修复路由",
			ActUninstall: "卸载",
			ActRetry:     "重试启动",
		},
	}
	// 激活配置的 node_id 是当前投影的权威身份。bundle 可能仍保留旧节点，
	// 不能用 bundle 的 self 行覆盖刚切换后的运行节点。
	if id := ActiveNodeID(home); id != "" {
		st.NodeID = id
	}

	// ── ① 装没装 ─────────────────────────────────────────
	st.GnbBin = p.gnbPath
	st.Installed = p.gnbPath != ""
	if !st.Installed {
		st.State = StateUninstalled
		st.OneLine = "未安装 gnb 驱动"
		st.Detail = "隧道驱动不在 PATH，也没有安装足迹"
		st.Actions[ActInstall] = true
		st.Actions[ActConfigure] = true // 可以先配置再装
		st.PrimaryAction = ActInstall
		return st
	}

	// ── ② 配没配 ─────────────────────────────────────────
	nodeConf := filepath.Join(gnbConf, "node.conf")
	// 配置树 v2+：节点唯一存放位 conf/<form>/<nodeId>/ 有身份材料即算已配（D4-E）；
	// 旧 keys/ 树与 mynet.conf 仅作兼容
	myConf := filepath.Join(conf, "mynet.conf")
	keysOK := nodeDirHasKeys(home, "local", st.NodeID) ||
		fileNonEmpty(filepath.Join(conf, "local", "nodes", st.NodeID, "keys", "node.private")) ||
		fileNonEmpty(filepath.Join(conf, "local", "keys", "node.private"))
	if !keysOK {
		// online：按 mode.json 的 node_id 找
		if id := ActiveNodeID(home); id != "" {
			if nodeDirHasKeys(home, "online", id) {
				keysOK = true
				if st.NodeID == "" {
					st.NodeID = id
				}
			}
			for _, p := range []string{
				filepath.Join(conf, "online", activeTenantID(home), "nodes", id, "keys", "node.private"),
				filepath.Join(conf, "online", activeTenantID(home), "keys", "node.private"),
			} {
				if fileNonEmpty(p) {
					keysOK = true
					if st.NodeID == "" {
						st.NodeID = id
					}
					break
				}
			}
		}
	}
	legacyOK := fileNonEmpty(myConf)
	st.Configured = fileNonEmpty(nodeConf) && (keysOK || legacyOK)

	// overlay 优先级：intent/route.conf 本机行 → bundle → 旧 mynet.conf
	st.OverlayIP = overlayFromRouteConf(filepath.Join(gnbConf, "route.conf"))
	if st.OverlayIP == "" {
		if li := readIntentOverlay(home); li != "" {
			st.OverlayIP = li
		}
	}
	if kv := readKV(filepath.Join(conf, "mynet.conf")); kv != nil {
		if st.NodeID == "" {
			st.NodeID = kv["NODE_ID"]
		}
		if st.OverlayIP == "" && kv["OVERLAY_IP"] != "" {
			st.OverlayIP = kv["OVERLAY_IP"]
		}
	}
	peers := loadPeerRoutes(bundlePath)
	if st.OverlayIP == "" && len(peers) > 0 {
		st.OverlayIP = peers[0].SelfIP
	}
	st.PeersTotal = len(peers)

	if !st.Configured {
		st.State = StateInstalled
		st.OneLine = "已安装，尚未配置节点"
		st.Detail = "缺 conf/mynet.conf 或 var/gnb/node.conf"
		st.Actions[ActConfigure] = true
		st.Actions[ActUninstall] = true
		st.PrimaryAction = ActConfigure
		return st
	}

	// ── ③ 起没起 ─────────────────────────────────────────
	// gnb -c 指**运行投影** /var/run/mynet/<id> 时 pid 落在投影目录；
	// 只读 conf 节点目录会误判 STOPPED（2026-10-15 实测）。
	pid := readGnbPID(filepath.Join(gnbConf, "gnb.pid"))
	if pid <= 0 {
		if id := st.NodeID; id != "" {
			if run, err := utils.RunProjectionPath(id); err == nil && run != "" {
				pid = readGnbPID(filepath.Join(run, "gnb.pid"))
			}
		}
	}
	st.ProcessPID = pid
	st.ProcessUp = pid > 0 && p.pidAlive(pid)

	// tun：按 overlay IP 反查真实接口（darwin 是 utunN，配置里写的是 gnb_tun）
	if st.OverlayIP != "" {
		st.TunIface = p.findIface(st.OverlayIP)
		st.TunUp = st.TunIface != ""
	}
	// 缺口⑥：隧道健康信号 —— tun 是否存在、挂没挂 VPN IP（期望口名来自 bundle；
	// 两者都未知时 Checked=false，如实报「无判据」）
	expectedTun, _, _ := tunNameFromBundle()
	st.Tunnel = computeTunHealth(st.OverlayIP, expectedTun, p.findIface, p.ifaceExists)
	// ④ 运行区：进程时长 + 接口计数器
	if st.ProcessUp && p.procUptimeSec != nil {
		st.UptimeSec = p.procUptimeSec(pid)
	}
	if st.TunUp && p.ifaceStats != nil {
		rx, tx, mtu, ok := p.ifaceStats(st.TunIface)
		if ok {
			st.RxBytes, st.TxBytes, st.Mtu = rx, tx, mtu
		}
	}

	// ⑤ 对端表 + 路由：每个对端 /32 都要在系统路由表里
	routed := 0
	var missing []string
	// 链路态来自 gnb_ctl；拿不到就留 unknown，不编
	var links map[int64]PeerLinkInfo
	if p.gnbPeerLinks != nil {
		// gnb -c 指运行投影时 gnb.map 在 /var/run/mynet/<id>/（不在 conf 节点目录）
		mapDir := gnbConf
		if id := st.NodeID; id != "" {
			if run, err := utils.RunProjectionPath(id); err == nil && run != "" {
				if _, serr := os.Stat(filepath.Join(run, "gnb.map")); serr == nil {
					mapDir = run
				}
			}
		}
		links = p.gnbPeerLinks(mapDir)
	}
	// 本机行在最前（与 route.conf 约定同形：第一条 = 本机）
	st.Peers = append(st.Peers, PeerRow{
		NodeID:  st.NodeID,
		Overlay: st.OverlayIP,
		IsSelf:  true,
		Routed:  st.TunUp, // 本机行的「路由」= tun 是否在
		Link:    "local",
	})
	for _, r := range peers {
		if r.PeerIP == "" {
			continue
		}
		ok := p.routeExists(r.PeerIP+"/32") || p.routeExists(r.PeerIP)
		// 对端可能宣告**网段**（route.conf 形如 192.168.0.0/24）。只查 /32 会把
		// 已装好的 /24 网段路由误报成缺失（实测 192.168.0.0）。
		if !ok && strings.HasSuffix(r.PeerIP, ".0") {
			ok = p.routeExists(r.PeerIP + "/24")
			if !ok {
				// darwin `route get 网段地址` 不可靠，用段内首主机探
				if v := net.ParseIP(r.PeerIP); v != nil && v.To4() != nil {
					b := v.To4()
					b[3] = 1
					ok = p.routeExists(b.String())
				}
			}
		}
		if !ok && p.localRouteExists != nil {
			ok = p.localRouteExists(r.PeerIP)
		}
		if ok {
			routed++
		} else {
			missing = append(missing, r.PeerIP)
		}
		row := PeerRow{
			NodeID:  r.NodeID,
			Overlay: r.PeerIP,
			Routed:  ok,
			Link:    "unknown", // gnb_ctl 不可用时如实说未知，别猜
		}
		if links != nil {
			if pid, err := strconv.ParseInt(r.NodeID, 10, 64); err == nil {
				if l, ok := links[pid]; ok {
					row.Link = l.Link
					row.LatencyMs = int(l.LatencyMs)
				}
			}
			if row.Link == "unknown" {
				// node_id 缺失/对不上时按 overlay 兜底
				for _, l := range links {
					if l.Overlay == r.PeerIP && l.Link != "" {
						row.Link = l.Link
						row.LatencyMs = int(l.LatencyMs)
						break
					}
				}
			}
		}
		st.Peers = append(st.Peers, row)
	}
	st.PeersRouted = routed
	st.MissingRoutes = append([]string(nil), missing...)
	st.RoutesOK = st.PeersTotal > 0 && routed == st.PeersTotal

	// route.conf 原文（⑤ 折叠区）—— 配置层面的「有没有」
	if b, err := os.ReadFile(filepath.Join(gnbConf, "route.conf")); err == nil {
		st.RouteConfText = string(b)
	}
	st.SystemRoutesText = dumpSystemRoutes()

	// ── ④ 判状态 ─────────────────────────────────────────
	switch {
	case !st.ProcessUp:
		// 配好了但没在跑。
		// pid 文件在而进程没了 = 异常退出或上次没干净退出；
		// 用户主动 Stop() 会清掉 pid 文件，走到下面的「已停止」。
		if pid > 0 {
			st.State = StateFailed
			st.OneLine = "隧道已退出"
			st.Detail = "进程不在，但 pid 文件仍在 —— 可能是异常退出。查 /var/log/mynet/gnb.err 与配置密钥。"
			st.Actions[ActRetry] = true
			st.Actions[ActStart] = true
			st.Actions[ActConfigure] = true
			st.Actions[ActUninstall] = true
			st.PrimaryAction = ActRetry
		} else {
			st.State = StateStopped
			st.OneLine = "已停止"
			st.Detail = "隧道未运行。可启动网络重新接入。"
			st.Actions[ActStart] = true
			st.Actions[ActConfigure] = true
			st.Actions[ActUninstall] = true
			st.PrimaryAction = ActStart
		}
		return st

	case !st.TunUp:
		st.State = StateDegraded
		st.OneLine = "进程在，但隧道接口没有 " + orDash(st.OverlayIP)
		st.Detail = "gnb 活着，却找不到挂了 overlay IP 的接口 —— 配置或密钥有问题"
		st.Actions[ActRestart] = true
		st.Actions[ActStop] = true
		st.Actions[ActFixRoutes] = true
		st.Actions[ActConfigure] = true
		st.PrimaryAction = ActRestart
		return st

	case st.PeersTotal > 0 && !st.RoutesOK:
		st.State = StateDegraded
		st.OneLine = "网络连接需要处理 · " + strconv.Itoa(len(missing)) + " 条路由未安装"
		st.Detail = "接口和网络地址正常，但这些目标没有可用路由：" + strings.Join(missing, "、") + "。"
		st.Actions[ActFixRoutes] = true
		st.Actions[ActRestart] = true
		st.Actions[ActStop] = true
		st.PrimaryAction = ActFixRoutes
		return st
	}

	st.State = StateRunning
	st.OneLine = "运行中 · " + orDash(st.TunIface) + " · " + orDash(st.OverlayIP) +
		" · 对端 " + strconv.Itoa(st.PeersRouted) + "/" + strconv.Itoa(st.PeersTotal) + " 在线"
	st.Actions[ActRestart] = true
	st.Actions[ActStop] = true
	st.Actions[ActConfigure] = true
	st.Actions[ActUninstall] = true
	st.PrimaryAction = ActRestart
	return st
}

// peerRoute 一对「本机 IP + 对端 IP」，用于路由存在性检查与对端表。
type statusPeerRoute struct {
	NodeID string
	SelfIP string
	PeerIP string
}

// loadPeerRoutes 从 bundle.json 读本机地址与对端 /32 列表。
// 对端的 node_id 来自 gnb.nodes（tun_address 与 routes.prefix 对得上时）。
func loadPeerRoutes(bundlePath string) []statusPeerRoute {
	b, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil
	}
	var env struct {
		Network struct {
			Addresses []string `json:"addresses"`
			Routes    []struct {
				Prefix string `json:"prefix"`
				Dev    string `json:"dev"`
			} `json:"routes"`
		} `json:"network"`
		Gnb struct {
			Nodes []struct {
				NodeID     string `json:"node_id"`
				TunAddress string `json:"tun_address"`
			} `json:"nodes"`
		} `json:"gnb"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return nil
	}
	self := ""
	if len(env.Network.Addresses) > 0 {
		self = strings.Split(env.Network.Addresses[0], "/")[0]
	}
	// IP → nodeID 映射（gnb.nodes 的 tun_address）
	idOf := map[string]string{}
	for _, n := range env.Gnb.Nodes {
		ip := strings.Split(n.TunAddress, "/")[0]
		if ip != "" {
			idOf[ip] = n.NodeID
		}
	}
	var out []statusPeerRoute
	for _, r := range env.Network.Routes {
		ip := strings.Split(r.Prefix, "/")[0]
		if ip == "" || ip == self {
			continue
		}
		out = append(out, statusPeerRoute{NodeID: idOf[ip], SelfIP: self, PeerIP: ip})
	}
	// 至少把本机地址带回去，供 OverlayIP 用
	if len(out) == 0 && self != "" {
		out = append(out, statusPeerRoute{SelfIP: self})
	}
	return out
}

func readGnbPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return n
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// handleNodeStatus GET /api/v1/node-status —— 状态机出口。

// overlayFromRouteConf route.conf 本机行（nodeid|ip|mask）取 overlay。
func overlayFromRouteConf(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(b), "\n") {
		parts := strings.Split(strings.TrimSpace(ln), "|")
		if len(parts) >= 2 && parts[1] != "" && !strings.Contains(parts[1], ":") {
			// 第一条主机行即本机（writeGnbProjection 会把本机放最前）
			return parts[1]
		}
	}
	return ""
}

func ActiveNodeID(home string) string {
	raw, err := readModeFile(home)
	if err != nil {
		return ""
	}
	var m struct {
		Active struct {
			NodeID string `json:"node_id"`
		} `json:"active"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Active.NodeID
}

func activeTenantID(home string) string {
	raw, err := readModeFile(home)
	if err != nil {
		return ""
	}
	var m struct {
		Active struct {
			TenantID string `json:"tenant_id"`
		} `json:"active"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Active.TenantID
}

func readIntentOverlay(home string) string {
	b, err := os.ReadFile(filepath.Join(home, "conf", "local", "intent.json"))
	if err != nil {
		return ""
	}
	var li struct {
		Node struct {
			Overlay string `json:"overlay"`
		} `json:"node"`
	}
	_ = json.Unmarshal(b, &li)
	return strings.TrimSuffix(li.Node.Overlay, "/32")
}

// cmdNodeStatusJSON — G3 执行器入口：mynet node-status --json
// 输出 ComputeNodeStatus 的 JSON，供 mynetd localapi 经 Ops 读取。
func cmdNodeStatusJSON(args []string) int {
	st := ComputeNodeStatus()
	home := os.Getenv("MYNET_HOME")
	if home == "" {
		home = st.Home
	}
	st.ActiveNodeID = ActiveNodeID(home)
	if st.ProcessUp && st.ActiveNodeID != "" && st.NodeID != "" && st.ActiveNodeID != st.NodeID {
		st.ConfigMismatch = true
		st.State = StateDegraded
		st.OneLine = "运行节点与激活配置不一致"
		st.Detail = "当前进程是 " + st.NodeID + "，激活配置是 " + st.ActiveNodeID + "；请切换并重新应用"
		st.PrimaryAction = ActRestart
		st.Actions[ActRestart] = true
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(st); err != nil {
		fmt.Fprintf(os.Stderr, "encode node-status: %v\n", err)
		return 1
	}
	return 0
}

// dumpSystemRoutes 系统 IPv4 路由表摘录（UI「系统路由」标签）。
// darwin/netstat 或 linux/route；失败返回空串，前端显示「不可用」。
func dumpSystemRoutes() string {
	if bin := lookPathAny("netstat"); bin != "" {
		out, err := exec.Command(bin, "-rn", "-f", "inet").Output()
		if err == nil {
			return clipText(string(out), 8000)
		}
	}
	if bin := lookPathAny("route"); bin != "" {
		out, err := exec.Command(bin, "-n", "get", "default").Output()
		if err == nil {
			return clipText(string(out), 4000)
		}
	}
	return ""
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…（已截断）"
}
