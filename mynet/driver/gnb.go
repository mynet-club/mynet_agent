package driver

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

// GNBDriver — A-3。只读 + 生命周期。
// 配置只进 driver/gnb/conf/<nodeId>/；密钥布局 clamp(SHA512(seed))（见 mynet/keys）。
type GNBDriver struct {
	NodeID  int64
	ConfDir string
	Exec    func(name string, args ...string) ([]byte, error)
	// Spawn 用于会自我 daemon 化的命令，语义见 spawnDetached。
	// 与 Exec 分开是因为两者的等待条件根本不同：Exec 要拿到输出，
	// Spawn 只要父进程退出。
	Spawn func(name string, args ...string) error
	// Start 之后确认进程真活着的探测次数与间隔。做成字段是为了测试能缩短 ——
	// 否则「起不来」那条用例要真等满 5s。零值表示用默认。
	StartProbes     int
	StartProbeDelay time.Duration
}

// Start 后确认存活的默认预算：25 × 200ms = 5s。
const (
	defaultStartProbes     = 25
	defaultStartProbeDelay = 200 * time.Millisecond
)

func NewGNBDriver(nodeID int64) *GNBDriver {
	return &GNBDriver{
		NodeID:          nodeID,
		ConfDir:         fmt.Sprintf("driver/gnb/conf/%d", nodeID),
		Exec:            execCombined,
		Spawn:           spawnDetached,
		StartProbes:     defaultStartProbes,
		StartProbeDelay: defaultStartProbeDelay,
	}
}

func (d *GNBDriver) GetDriverType() domain.VPNDriverType { return domain.DriverTypeGNB }

// gnbCtlStatusArgs 返回读取状态所需的完整参数。
//
// ⚠️ 三个 flag 缺一不可，而且**不能只看退出码**：
//   - 缺 `-b <conf>/gnb.map`：gnb_ctl 只打印 usage 就 **exit 0**（实测），
//     于是「可用」恒为真，而 GetAllPeers 会把那段 usage 文本当状态解析出 0 个 peer
//   - 缺 `-c`：只输出头部（conf_dir / node_num / wan*_port），**没有任何 node 段**
//   - map 是 0600 root:root（gnb 以 root 跑）：非 root 读到
//     `open ctl block error`，而 gnb_ctl **仍然 exit 0**
//
// 所以这里用「输出里必须有 node_num[ 这一行」作为真的读到了状态的判据 ——
// 它是 gnb_ctl_dump_status 的头部字段（gnb_ctl_dump.c），usage 与错误信息里都没有。
func (d *GNBDriver) gnbCtlStatusArgs() []string {
	return []string{"-b", d.ConfDir + "/gnb.map", "-c", "-s"}
}

func (d *GNBDriver) ctlStatus() (string, error) {
	out, err := d.Exec("gnb_ctl", d.gnbCtlStatusArgs()...)
	if err != nil {
		return "", fmt.Errorf("gnb_ctl %v: %w", d.gnbCtlStatusArgs(), err)
	}
	s := string(out)
	if !strings.Contains(s, "node_num[") {
		return "", fmt.Errorf("gnb_ctl 没有返回状态（退出码 0 但输出不含 node_num[）：%s",
			firstLine(strings.TrimSpace(s)))
	}

	return s, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + "…"
	}
	if len(s) > 80 {
		return s[:80] + "…"
	}

	return s
}

func (d *GNBDriver) IsAvailable() bool {
	_, err := d.ctlStatus()

	return err == nil
}

// IsProcessAlive 按自己的 pid 文件判活。
//
// ⚠️ 此前是 `pgrep -f gnb_es` —— gnb_es 是上游的 endpoint scanner 工具，
// **不是**主守护进程（主进程是 gnb）。用它会得到两种错：本机跑着 gnb 却报 not alive，
// 以及别的节点上恰好有 gnb_es 时误报 alive。
func (d *GNBDriver) IsProcessAlive() bool {
	pid, err := d.readPidFile()
	if err != nil {
		return false
	}
	// kill -0 只探测存在性与权限，不发信号
	_, err = d.Exec("kill", "-0", strconv.Itoa(pid))

	return err == nil
}

func (d *GNBDriver) Capabilities() domain.DriverCaps {
	// HotReload：gnb_ctl -w core.flag=2 未验证 → 如实 false
	return domain.DriverCaps{
		HotReload:       false,
		Discovery:       true,
		Proxy:           true,
		CustomKeyUpload: true,
	}
}

func (d *GNBDriver) Refresh() error {
	_, err := d.ctlStatus()

	return err
}

func (d *GNBDriver) GetAllPeers() ([]*domain.VPNPeer, error) {
	out, err := d.ctlStatus()
	if err != nil {
		return nil, err
	}

	return parseGNBStatus(out), nil
}

func (d *GNBDriver) GetOnlinePeers() ([]*domain.VPNPeer, error) {
	all, err := d.GetAllPeers()
	if err != nil {
		return nil, err
	}
	var up []*domain.VPNPeer
	for _, p := range all {
		if p.IsOnline() {
			up = append(up, p)
		}
	}
	return up, nil
}

func (d *GNBDriver) GetPeer(peerID int64) (*domain.VPNPeer, error) {
	all, err := d.GetAllPeers()
	if err != nil {
		return nil, err
	}
	for _, p := range all {
		if p.PeerID == peerID || p.NodeID == peerID {
			return p, nil
		}
	}
	return nil, fmt.Errorf("peer %d not found", peerID)
}

func (d *GNBDriver) GetLocalPeer() (*domain.VPNPeer, error) {
	return &domain.VPNPeer{NodeID: d.NodeID, IsLocal: true}, nil
}

func (d *GNBDriver) GetNetworkSummary() (*domain.VPNNetworkSummary, error) {
	peers, err := d.GetAllPeers()
	if err != nil {
		return nil, err
	}
	on, _ := d.GetOnlinePeers()
	return &domain.VPNNetworkSummary{
		TotalPeers:     len(peers),
		OnlinePeers:    len(on),
		IsProcessAlive: d.IsProcessAlive(),
		LastUpdate:     time.Now(),
	}, nil
}

// WriteConfig 旧契约 VPNBundle → node.conf 等。
func (d *GNBDriver) WriteConfig(b *domain.VPNBundle) error {
	if b == nil {
		return fmt.Errorf("gnb: nil bundle")
	}
	text := RenderGNBNodeConfFromBundle(b)
	return writeFile0600(d.ConfDir+"/node.conf", []byte(text))
}

// WriteIntent GnbIntent → gnb 原生配置目录。
//
// 每个文件的格式都对着 gnb 源码核实过，并拿一份**真跑通过的**配置比对过
// （旧 U1 mesh 的 /tmp/t1-gnb/3001/），不是猜的：
//
//	node.conf                nodeid/ifname/listen/mtu/daemon/…   gnb_conf_file.c:269,293,300,452,464
//	route.conf               <uuid>|<ip>|<点分掩码>               gnb_conf_file.c:919-928
//	address.conf             <attrib>|<uuid>|<host>|<port>       gnb_conf_file.c:50-56
//	security/<uuid>.private  128 hex，**无换行**                  gnb_keys.c:60,68-73
//	security/<uuid>.public    64 hex，**无换行**                  gnb_keys.c:61,84-89
//	ed25519/<peer>.public     64 hex，**无换行**                  gnb_keys.c:103,108-112
//
// pid-file / ctl-block 刻意不写：用 `-c <conf_dir>` 时 gnb 自己默认成
// <conf_dir>/gnb.pid 与 <conf_dir>/gnb.map（gnb_argv.c:855,857-858）。
// 那份真配置也没写这两项，gnb.map 照样生成了。
//
// ⚠️ 此前这里只写 node.conf + route.conf，而且两者格式都是错的：
// `node <id>`（gnb 认的键是 `nodeid`，strncmp 不匹配 → 静默丢弃）、
// route 用「空格 + CIDR」（gnb 只认 `|` 或 `/` 分隔的三段，否则 num!=3 → continue，
// **整份 route.conf 全被跳过**）；address.conf 与两个密钥目录则根本没写。
// 于是配置"写成功了"、refresh-config 退出码 0，但隧道起不来。
func (d *GNBDriver) WriteIntent(g *domain.GnbIntent) error {
	if g == nil {
		return fmt.Errorf("gnb: nil intent")
	}
	if g.Identity.NodeID == "" {
		return fmt.Errorf("gnb: intent 缺 identity.node_id")
	}
	if err := checkGnbIfname(g); err != nil {
		return err
	}

	if err := writeFile0600(d.ConfDir+"/node.conf", []byte(RenderGNBNodeConf(g))); err != nil {
		return err
	}
	// route.conf 必须存在：缺文件 gnb 直接退出（gnb_conf_file.c:897-901）。
	// 没有对端时也要写个空文件，而不是不写。
	if err := writeFile(d.ConfDir+"/route.conf", []byte(RenderGNBRouteConf(g)), 0o644); err != nil {
		return err
	}
	if addr := RenderGNBAddressConf(g); addr != "" {
		if err := writeFile(d.ConfDir+"/address.conf", []byte(addr), 0o644); err != nil {
			return err
		}
	}

	return d.writeGnbKeys(g)
}

// checkGnbIfname 挡掉超长接口名。
//
// gnb 的 ifname 缓冲区是 16 字节且硬截断到 15 字符
// （gnb_conf_file.c:275-277：memset(…,0,16) + strncpy(…,16) + ifname[15]='\0'），
// Linux 的 IFNAMSIZ 同样是 16。超长名字会被**静默截断**成一个谁也没预期的接口名，
// 之后所有按名字找接口的操作全都对不上。宁可在这里报错。
func checkGnbIfname(g *domain.GnbIntent) error {
	ifname := gnbIfname(g)
	if ifname == "" {
		return nil // 空 → 不写 ifname 键，gnb 用自己的默认 gnb_tun（gnb_argv.c:224）
	}
	if len(ifname) > 15 {
		return fmt.Errorf("gnb: ifname %q 长 %d 字符，超过上限 15（会被静默截断）", ifname, len(ifname))
	}

	return nil
}

func gnbIfname(g *domain.GnbIntent) string {
	if g.Options != nil && g.Options.Ifname != "" {
		return g.Options.Ifname
	}

	return ""
}

// writeGnbKeys 落盘本机密钥与对端公钥。
//
// ⚠️ 必须是**纯 hex 无换行**：gnb 用裸 read() 且要求读满
// 128（私钥）/ 64（公钥）字节，读不满就 exit(0)（gnb_keys.c:68-73, 84-89, 108-112）。
// gnb_crypto 自己也是 write(fd, hex, 128) 不带换行，与之对齐。
func (d *GNBDriver) writeGnbKeys(g *domain.GnbIntent) error {
	self := g.Identity.NodeID
	if g.Identity.PrivateKey != "" {
		if err := writeFile0600(d.ConfDir+"/security/"+self+".private",
			[]byte(strings.TrimSpace(g.Identity.PrivateKey))); err != nil {
			return err
		}
	}
	if g.Identity.PublicKey != "" {
		if err := writeFile0600(d.ConfDir+"/security/"+self+".public",
			[]byte(strings.TrimSpace(g.Identity.PublicKey))); err != nil {
			return err
		}
	}
	for peer, pub := range g.Identity.PeerPublicKeys {
		if peer == "" || strings.TrimSpace(pub) == "" {
			continue
		}
		if err := writeFile0600(d.ConfDir+"/ed25519/"+peer+".public",
			[]byte(strings.TrimSpace(pub))); err != nil {
			return err
		}
	}

	return nil
}

// Start 拉起 gnb 守护进程。
//
// ⚠️ 此前是 `gnb_ctl -b <conf>/gnb.map` —— 那不是启动命令：`-b` 是 `--ctl-block`，
// 只指定"打开哪块共享内存来读"（gnb_ctl.c:85,107-109）；而且 gnb_ctl 在没有
// `-s`/`-a` 时只打开映射就 return 0（gnb_ctl.c:149-183），文件不存在也只是打印
// error 后 exit(0) —— 于是 Start() 会**假装成功**。
// 真正的守护进程是 `gnb -c <conf_dir>`（gnb_argv.c:899）。
//
// node.conf 里写 `daemon on`，gnb 双 fork 后父进程立即 exit(0)（gnb.c:397-399 →
// gnb_daemon.c:36-48），所以这里的 Exec 不会阻塞；子进程随后写 <conf>/gnb.pid
// （gnb.c:401）。被 systemd 用 `gnb --systemd` 拉起时该键被忽略、保持前台受监管
// （gnb_argv.c:439-440 + gnb_conf_file.c:458），正是 E-2 那个模型。
func (d *GNBDriver) Start() error {
	if d.IsProcessAlive() {
		return nil
	}
	if err := d.Spawn("gnb", "-c", d.ConfDir); err != nil {
		return fmt.Errorf("gnb 启动失败: %w", err)
	}
	// ⚠️ Spawn 返回 nil **不代表起来了**。`daemon on` 时父进程在 fork 后立刻
	// exit(0)（gnb_daemon.c:40-42），真正读配置、建 tun 的是子进程 ——
	// 子进程失败时 gnb 是 `exit(0)` 而不是非零码（gnb_keys.c:66,72,78 等），
	// 错误根本传不回父进程。所以必须回头确认真的活着。
	probes, delay := d.StartProbes, d.StartProbeDelay
	if probes <= 0 {
		probes = defaultStartProbes
	}
	if delay <= 0 {
		delay = defaultStartProbeDelay
	}
	for i := 0; i < probes; i++ {
		if d.IsProcessAlive() {
			return nil
		}
		time.Sleep(delay)
	}

	return fmt.Errorf("gnb 启动后 %s 内没有活进程；配置有误时 gnb 子进程会直接退出且"+
		"错误报不回来，请检查 %s 下的 node.conf/route.conf/address.conf 与 security/ 密钥",
		time.Duration(probes)*delay, d.ConfDir)
}

// Stop 按 pid 文件停，不按名字（AGENTS.md §3：多实例可同名并存，禁 pkill/killall）。
//
// ⚠️ 此前是 `gnb_ctl -k`，而 gnb_ctl 的 getopt 串是 "b:n:csa:o46h"
// （gnb_ctl.c:102），long_options 里也没有 k —— **这个 flag 根本不存在**。
// gnb 装了 SIGTERM handler（gnb.c:395），发 SIGTERM 即可优雅退出。
func (d *GNBDriver) Stop() error {
	pid, err := d.readPidFile()
	if err != nil {
		return err
	}
	_, err = d.Exec("kill", "-TERM", strconv.Itoa(pid))

	return err
}

// readPidFile 读 <ConfDir>/gnb.pid —— gnb 用 -c 时的默认 pid 路径
// （gnb_argv.c:857-858）。
func (d *GNBDriver) readPidFile() (int, error) {
	raw, err := os.ReadFile(d.ConfDir + "/gnb.pid")
	if err != nil {
		return 0, fmt.Errorf("gnb: 读 pid 文件失败（进程可能没在跑）: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("gnb: pid 文件内容非法: %q", strings.TrimSpace(string(raw)))
	}

	return pid, nil
}

// Reload 退化为 Stop+Start（热重启未验证）。
func (d *GNBDriver) Reload() error {
	_ = d.Stop()
	return d.Start()
}

func (d *GNBDriver) Status() (*domain.DriverStatus, error) {
	return &domain.DriverStatus{
		Running: d.IsProcessAlive(),
		NodeID:  strconv.FormatInt(d.NodeID, 10),
	}, nil
}

// parseGNBStatus 解析 `gnb_ctl -s` 的输出。
//
// 格式取自上游源码 `mynet_gnb/src/src/ctl/gnb_ctl_dump.c:38-180`
// （`gnb_ctl_dump_status`）—— 不是猜的：
//
//	conf->conf_dir[/etc/gnb/conf]
//	node_num[2]
//	wan6_port[7676]
//	wan4_port[7676]
//
//	====================
//	node 1000000000000001
//	addr4_ping_latency_usec 1234
//	tun_ipv6 2001:db8::1
//	tun_ipv4 10.1.0.1
//	in  4096 (4.000K bytes)
//	out 2048 (2.000K bytes)
//	public_key <hex>
//	ipv6 Direct Point to Point
//	ipv4 InDirect
//	ping_ts_sec:1699999999(2026-09-24 10:00:00)
//	detect_count 5
//	available_address6:
//	address ...
//
// **peer 的边界是 `node <uuid>` 行**；`====` 只是分隔符，而头部的
// conf_dir / node_num / wan*_port 不属于任何 peer。
//
// 在线判据用 `Direct Point to Point`（源码 :121-135：PONG 位置位才打这行）；
// `InDirect` 表示走中继 → relay；`Local node` 是本机自己。
//
// ⚠️ 早先的实现把**每一行**都当成一个 peer（只按含不含 "PONG" 判状态），于是
// 两个节点的输出会报出五十多个 peer —— 头部行、分隔行、每个字段行全被算进去。
// 而它的测试夹具是 `"peer 1 PONG\npeer 2 down\n"`，那**不是 gnb_ctl 的输出格式**。
// 解析器和测试一起错，所以一直没暴露。夹具现已换成按上游源码逐行推导的真实形状。
func parseGNBStatus(out string) []*domain.VPNPeer {
	var peers []*domain.VPNPeer
	var cur *domain.VPNPeer

	flush := func() {
		if cur != nil {
			peers = append(peers, cur)
			cur = nil
		}
	}

	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "====") {
			continue
		}

		if strings.HasPrefix(line, "node ") {
			flush()
			cur = &domain.VPNPeer{ConnectionStatus: "offline"}
			if n, err := strconv.ParseInt(strings.TrimSpace(line[len("node "):]), 10, 64); err == nil {
				cur.PeerID = n
				cur.NodeID = n
			}
			continue
		}
		if cur == nil {
			continue // 头部行（conf_dir / node_num / wan*_port）不属于任何 peer
		}

		switch {
		case strings.HasPrefix(line, "tun_ipv4 "):
			cur.VirtualIP = afterKey(line)
		case strings.HasPrefix(line, "tun_ipv6 "):
			cur.VirtualIP6 = afterKey(line)
		case strings.HasPrefix(line, "ipv6 Local node"), strings.HasPrefix(line, "ipv4 Local node"):
			cur.IsLocal = true
		case strings.HasPrefix(line, "ipv6 Direct Point to Point"):
			markDirect(cur, "direct_ipv6")
		case strings.HasPrefix(line, "ipv4 Direct Point to Point"):
			markDirect(cur, "direct_ipv4")
		case strings.HasPrefix(line, "ipv6 InDirect"), strings.HasPrefix(line, "ipv4 InDirect"):
			// 走中继：连得上但不是直连。已被判为 online 的不要被降级。
			if cur.ConnectionStatus == "offline" {
				cur.ConnectionStatus = "relay"
			}
		case strings.HasPrefix(line, "in "), strings.HasPrefix(line, "in  "):
			cur.RxBytes = firstUint(afterKey(line))
		case strings.HasPrefix(line, "out "):
			cur.TxBytes = firstUint(afterKey(line))
		case strings.HasPrefix(line, "addr4_ping_latency_usec"):
			// 源码里这行出现两次（:92 空格分隔、:140 冒号分隔），值相同，取第一个
			if cur.LatencyMs == 0 {
				cur.LatencyMs = float64(firstUint(afterKey(line))) / 1000
			}
		}
	}
	flush()
	return peers
}

// markDirect 置为直连在线；v4/v6 都直连时类型是 dual_stack。
func markDirect(p *domain.VPNPeer, typ string) {
	p.ConnectionStatus = "online"
	switch {
	case p.ConnectionType == "":
		p.ConnectionType = typ
	case p.ConnectionType != typ:
		p.ConnectionType = "dual_stack"
	}
}

// afterKey 剥掉「key」部分，返回其后的内容。同时吃得下源码里的两种分隔：
// "tun_ipv4 10.1.0.1"（空格）与 "addr4_ping_latency_usec:1500"（冒号）。
//
// ⚠️ 必须先剥 key 再找数字 —— `addr4_ping_latency_usec` 这个**键名里就带 4**，
// 直接对整行找第一段数字会拿到 4 而不是 1500（这个 bug 是测试抓出来的）。
func afterKey(line string) string {
	if i := strings.IndexAny(line, " \t:"); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return ""
}

// firstUint 取字符串里第一段连续数字。
// 配合 afterKey 吃得下 "4096 (4.000K bytes)" 和 "1699999999(2026-...)" 这两种形状。
func firstUint(s string) uint64 {
	i := 0
	for i < len(s) && !isDigit(s[i]) {
		i++
	}
	j := i
	for j < len(s) && isDigit(s[j]) {
		j++
	}
	if i == j {
		return 0
	}
	n, _ := strconv.ParseUint(s[i:j], 10, 64)
	return n
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// RenderGNBNodeConf GnbIntent → node.conf。
//
// 键名与取值格式逐条对着 gnb 的解析器核实：
//
//	nodeid <十进制>            gnb_conf_file.c:293   sscanf("%32[^ ] %llu")
//	ifname <≤15 字符>          gnb_conf_file.c:269   sscanf("%32[^ ] %16s")，16 字节缓冲硬截断
//	listen <port | host:port>  gnb_conf_file.c:300 + gnb_conf.c:71-82（含 ':' 返 4 → v4 分支）
//	mtu <int>                  gnb_conf_file.c:465
//	daemon on|off              gnb_conf_file.c:452-462
//	multi-socket on|off        gnb_conf_file.c:356-366（off 走 else 置 0，安全）
//	safe-index on|off          gnb_conf_file.c:555-561
//	direct-forwarding on|off   gnb_conf_file.c:381-387
//	standard-forwarding on|off gnb_conf_file.c:369-375
//	unified-forwarding <v>     gnb_conf_file.c:393-410（off/auto/force/super/hyper，未识别回落 auto）
//
// ⚠️ 此前写的是 `node <id>`。gnb 认的键是 `nodeid`：
// strncmp("node 1001", "nodeid", 6) 比的是 "node 1" vs "nodeid" → 不匹配 →
// 这一行被**静默跳过**，本机 uuid 根本没设上。没有报错，只是隧道不通。
func RenderGNBNodeConf(g *domain.GnbIntent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "nodeid %s\n", g.Identity.NodeID)
	if ifname := gnbIfname(g); ifname != "" {
		fmt.Fprintf(&b, "ifname %s\n", ifname)
	}
	for _, ln := range g.Listeners {
		if ln = strings.TrimSpace(ln); ln != "" {
			fmt.Fprintf(&b, "listen %s\n", ln)
		}
	}
	// daemon on：自己起时 gnb 双 fork、父进程立即 exit(0)，Start() 不会阻塞；
	// 被 systemd 以 --systemd 拉起时这个键被忽略、保持前台受监管。两种模式都对。
	b.WriteString("daemon on\n")

	if g.Options != nil {
		if g.Options.MTU > 0 {
			fmt.Fprintf(&b, "mtu %d\n", g.Options.MTU)
		}
		fmt.Fprintf(&b, "multi-socket %s\n", onOff(g.Options.MultiSocket))
		fmt.Fprintf(&b, "safe-index %s\n", onOff(g.Options.SafeIndex))
		if p := strings.TrimSpace(g.Options.Passcode); p != "" {
			fmt.Fprintf(&b, "passcode %s\n", p)
		}
	}
	if g.Forwarding != nil {
		fmt.Fprintf(&b, "direct-forwarding %s\n", onOff(g.Forwarding.Direct))
		fmt.Fprintf(&b, "standard-forwarding %s\n", onOff(g.Forwarding.Standard))
		if u := strings.TrimSpace(g.Forwarding.Unified); u != "" {
			fmt.Fprintf(&b, "unified-forwarding %s\n", u)
		}
	}

	return b.String()
}

func onOff(v bool) string {
	if v {
		return "on"
	}

	return "off"
}

// RenderGNBRouteConf GnbIntent → route.conf，每行 `<uuid>|<ip>|<点分掩码>`。
//
// 格式取自 gnb_conf_file.c:919-928：先 gnb_test_field_separator 判分隔符，
// 只认 `/` 或 `|`；随后 sscanf 三段（uuid / ip / netmask），`3 != num` 就 continue。
// 掩码是**点分十进制**（后面直接 inet_pton(AF_INET, …)），不是 CIDR 前缀长度。
//
// ⚠️ 必须包含**本机自己**那一行：gnb 是靠 route.conf 里 uuid == 本机 的那条
// 来设自己的 tun 地址的（:938-946 的 `0 != p[3] && 0 == node->tun_addr4.s_addr` 分支）。
// 旧 U1 mesh 那份真跑通的配置第一行就是 `3001|10.31.0.1|255.255.255.0`（3001 是自己）。
// bundle 的 gnb.routes 只含对端，本机地址在 gnb.nodes 里 —— 所以这里以 nodes 为主源。
func RenderGNBRouteConf(g *domain.GnbIntent) string {
	var b strings.Builder
	seen := make(map[string]bool, len(g.Nodes)+len(g.Routes))

	emit := func(nodeID, cidr string) {
		if nodeID == "" || cidr == "" || seen[nodeID] {
			return
		}
		ip, mask, ok := splitCIDR(cidr)
		if !ok {
			return
		}
		seen[nodeID] = true
		fmt.Fprintf(&b, "%s|%s|%s\n", nodeID, ip, mask)
	}

	// g.Nodes 含本机与对端（publisher 两边都放），是 route.conf 的主源。
	// g.Routes 只含对端 —— projector 明确跳过自环（NetworkSemanticsProjector:113-115），
	// 所以本机那条不可能从 Routes 来，只能靠 Nodes。
	for _, n := range g.Nodes {
		emit(n.NodeID, n.TunAddress)
	}
	// gnb.routes 里可能有 nodes 没覆盖到的前缀（例如对端公告的整个网段）
	for _, r := range g.Routes {
		emit(r.NodeID, r.Prefix)
	}

	return b.String()
}

// splitCIDR "10.0.0.16/32" → ("10.0.0.16", "255.255.255.255", true)。
func splitCIDR(cidr string) (string, string, bool) {
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return "", "", false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String(), net.IP(ipnet.Mask).To4().String(), true
	}

	// gnb 的 route.conf 只走 inet_pton(AF_INET, …)（gnb_conf_file.c:933-934），
	// IPv6 前缀在这里落不了地 —— 如实跳过，不要写出一行会被静默 continue 的东西。
	return "", "", false
}

// RenderGNBAddressConf GnbIntent → address.conf，每行 `<attrib>|<uuid>|<host>|<port>`。
//
// 格式取自 gnb_conf_file.c:50-56（sscanf "%16[^|]|%llu|%46[^|]|%hu"，4 段缺一不可）。
// attrib 里只有 i/u/r/s/f 会置节点类型（:~95-115）；普通直连对端用别的字母即可，
// 地址照样进该节点的 static_address_list。上游官方示例是 `i|0|101.32.178.3|9001`，
// 旧 U1 mesh 那份真配置用的是 `n|3002|192.168.0.16|9202`。
//
// 没有 address.conf 时 gnb 直接 return（:59-63），不报错 —— 但节点就**没有任何
// 对端传输地址**，只能靠 index 发现。这是「配置写成功了却连不上」的又一形态。
func RenderGNBAddressConf(g *domain.GnbIntent) string {
	if g.Discovery == nil {
		return ""
	}
	var b strings.Builder

	for _, s := range g.Discovery.StaticNodes {
		host, port, ok := splitHostPort(s.Endpoint)
		if !ok || s.NodeID == "" {
			continue
		}
		role := strings.TrimSpace(s.Role)
		if role == "" {
			role = "n"
		}
		fmt.Fprintf(&b, "%s|%s|%s|%s\n", role, s.NodeID, host, port)
	}
	// index 服务：uuid 用 0（与上游官方示例一致），attrib 必须是 i 才会进
	// index_address_ring（gnb_conf_file.c:~95-98）
	for _, ep := range g.Discovery.IndexServers {
		host, port, ok := splitHostPort(ep)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "i|0|%s|%s\n", host, port)
	}

	return b.String()
}

// splitHostPort "192.168.0.16:9016" → ("192.168.0.16", "9016", true)。
// 也吃得下 [::1]:9016 与裸端口。
func splitHostPort(ep string) (string, string, bool) {
	ep = strings.TrimSpace(ep)
	if ep == "" {
		return "", "", false
	}
	if h, p, err := net.SplitHostPort(ep); err == nil && h != "" && p != "" {
		return h, p, true
	}
	// 裸端口：gnb 的 address.conf 必须要 host，给不出就如实失败
	if _, err := strconv.Atoi(ep); err == nil {
		return "", "", false
	}

	return "", "", false
}

func RenderGNBNodeConfFromBundle(b *domain.VPNBundle) string {
	var sb strings.Builder
	// 同 RenderGNBNodeConf：键名是 nodeid，不是 node
	fmt.Fprintf(&sb, "nodeid %s\n", b.Node.ID)
	if b.Node.ListenPort > 0 {
		fmt.Fprintf(&sb, "listen %d\n", b.Node.ListenPort)
	}
	sb.WriteString("daemon on\n")

	return sb.String()
}
