package driver

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// GNBDriver — A-3。只读 + 生命周期。
// 配置只进**节点唯一存放位** conf/<form>/<nodeId>/（D4-E，协议内布局不变：
// node.conf/route.conf/address.conf/security/<id>.{private,public}/ed25519/*.public/gnb.map）；
// 密钥布局 clamp(SHA512(seed))（见 mynet/keys）。
type GNBDriver struct {
	NodeID  int64
	ConfDir string
	// Binary gnb 可执行文件路径。空则 resolveGNBBinary（MYNET_GNB_BIN → PATH → 常见安装位）。
	// Windows 上 gnb 常装在 C:\gnb-win\bin\，不在 PATH —— 只 Spawn("gnb") 必失败。
	Binary string
	Exec   func(name string, args ...string) ([]byte, error)
	// Spawn 用于会自我 daemon 化的命令，语义见 spawnDetached。
	// 与 Exec 分开是因为两者的等待条件根本不同：Exec 要拿到输出，
	// Spawn 只要父进程退出。
	Spawn func(name string, args ...string) error
	// Start 之后确认进程真活着的探测次数与间隔。做成字段是为了测试能缩短 ——
	// 否则「起不来」那条用例要真等满 5s。零值表示用默认。
	StartProbes     int
	StartProbeDelay time.Duration
	// ServiceUnit gnb 的 systemd 单元名。非空且该单元已 load 时，
	// Start/Stop/IsProcessAlive 一律委托 systemctl —— 理由见 systemdManages 的注释：
	// `gnb --systemd` 不写 pid 文件，不委托就会 spawn 出**第二个 gnb**。
	// 置空表示「不用 systemd，直接 spawn」（容器内 / macOS / 手工调试）。
	ServiceUnit string
}

// DefaultGNBServiceUnit deploy/systemd/mynet_gnb.service 装出来的单元名。
const DefaultGNBServiceUnit = "mynet_gnb.service"

// Start 后确认存活的默认预算：25 × 200ms = 5s。
const (
	defaultStartProbes     = 25
	defaultStartProbeDelay = 200 * time.Millisecond
)

func NewGNBDriver(nodeID int64) *GNBDriver {
	return &GNBDriver{
		NodeID:          nodeID,
		ConfDir:         defaultGnbConfDir(nodeID),
		Exec:            execCombined,
		Spawn:           spawnDetached,
		StartProbes:     defaultStartProbes,
		StartProbeDelay: defaultStartProbeDelay,
		ServiceUnit:     DefaultGNBServiceUnit,
	}
}

// defaultGnbConfDir 默认 conf 目录 = **节点唯一存放位** conf/<form>/<nodeID>/
// （D4-E；form 由 mode.json 判，读不到按 local）。纯取值不建目录。
func defaultGnbConfDir(nodeID int64) string {
	form, _ := utils.ActiveNodeInfo()
	if nodeID > 0 {
		if p, err := utils.NodeRuntimePath(form, strconv.FormatInt(nodeID, 10)); err == nil {
			return p
		}
	}
	if p, err := utils.NodeRuntimePath(form, "default"); err == nil {
		return p
	}
	return ""
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

// IsProcessAlive 判活。systemd 托管时问 systemctl，否则按自己的 pid 文件 + `kill -0`。
//
// ⚠️ 此前是 `pgrep -f gnb_es` —— gnb_es 是上游的 endpoint scanner 工具，
// **不是**主守护进程（主进程是 gnb）。用它会得到两种错：本机跑着 gnb 却报 not alive，
// 以及别的节点上恰好有 gnb_es 时误报 alive。
func (d *GNBDriver) IsProcessAlive() bool {
	if d.systemdManages() {
		out, err := d.Exec("systemctl", "is-active", d.ServiceUnit)

		return err == nil && strings.TrimSpace(string(out)) == "active"
	}
	pid, err := d.readPidFile()
	if err != nil {
		return false
	}
	return d.pidAliveOS(pid)
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

// checkGnbIfname 挡掉会被静默截断或拼坏命令的接口名（规则在 domain.ValidateTunDevice，
// 与路由/proxy 那条解析链同源）。
//
// gnb 的 ifname 缓冲区是 16 字节且硬截断到 15 字符
// （gnb_conf_file.c:275-277：memset(…,0,16) + strncpy(…,16) + ifname[15]='\0'），
// Linux 的 IFNAMSIZ 同样是 16。超长名字会被**静默截断**成一个谁也没预期的接口名，
// 之后所有按名字找接口的操作全都对不上。宁可在这里报错。
//
// 空值在这里是允许的，因为本函数也服务**本地意图**（apply --active 的 local 模式，
// 没有后端参与，只能由 gnb 自己决定默认名）。从 bundle 来的路径不会走到「空」：
// project_active / refresh-config 用 node.interface 覆盖过 Options.Ifname，
// 解析不出来时直接报错退出（见 domain.BundleEnvelope.TunDevice）。
func checkGnbIfname(g *domain.GnbIntent) error {
	ifname := gnbIfname(g)
	if ifname == "" {
		return nil
	}

	return domain.ValidateTunDevice(ifname)
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
// systemdManages 报告 gnb 是否已由 systemd 单元托管。
//
// ⚠️ 这一步**必须判**，不是优化：`gnb --systemd` 会忽略 node.conf 的 `pid-file` 键
// （gnb_conf_file.c:349 的条件带 `&& 0 == conf->systemd_daemon`；同理 :336 的
// ctl-block）。于是 systemd 托管时**根本没有 pid 文件** → readPidFile() 失败 →
// IsProcessAlive() 恒 false → Start() 会 spawn **第二个 gnb**，两个进程抢同一个
// tun 与 UDP 端口，症状是隧道时通时断、且看日志两边都"正常"。
//
// 判据用 `systemctl show -p LoadState --value <unit>` == "loaded"：
// 单元不存在时它是 "not-found"，与「单元存在但没在跑」（inactive）区分得开。
// `systemctl is-active` 区分不了这两种（都输出 inactive/unknown + 非零退出码）。
//
// 委托给 systemctl 正是 D-7 对 `mynet service` 的原始定义
// （「进程生命周期（委托 procd/systemd/launchd）」）；直接 spawn 只是
// 没有服务管理器时的退路（手工调试、容器内、macOS）。
func (d *GNBDriver) systemdManages() bool {
	if d.ServiceUnit == "" {
		return false
	}
	out, err := d.Exec("systemctl", "show", "-p", "LoadState", "--value", d.ServiceUnit)
	if err != nil {
		return false
	}

	return strings.TrimSpace(string(out)) == "loaded"
}

// Start 拉起 gnb。整段在**每 confdir 一把**的生命周期锁内执行（见 withLifecycle）。
func (d *GNBDriver) Start() error { return d.withLifecycle(d.startLocked) }

func (d *GNBDriver) startLocked() error {
	log := logger.Comp("driver")
	// 平台服务管理器在（OpenWrt/procd）→ 交给它：绕过它自己 spawn 出的 gnb 它不认，
	// 两份争 tun 就是双 gnb 的来源（与 Lua 执行器委托 /etc/init.d/mynet 同一规则）。
	if handled, err := delegatePlatformService(d.Exec, "start"); handled {
		return err
	}
	if d.IsProcessAlive() {
		log.Debug("gnb 已在运行", "conf", d.ConfDir)
		return nil
	}
	// 启动前收敛：同 confdir 只许一个实例。**不能只信 pid 文件** —— 判活用的 pid 可能
	// 指错或被旧进程占着（2026-10-11 生产 .180：gnb.pid 记的正是 tunless 那个，持 tun 的
	// 是没被记录的孤儿）。命令行才是权威判据。
	if n := d.convergeSameConfdir(0, "启动前收敛"); n > 0 {
		log.Info("启动前收敛了同 confdir 遗留 gnb", "n", n, "conf", d.ConfDir)
	}
	log.Info("gnb 启动", "conf", d.ConfDir, "systemd", d.systemdManages())
	if d.systemdManages() {
		_, err := d.Exec("systemctl", "start", d.ServiceUnit)
		if err != nil {
			log.Error("systemctl start 失败", "unit", d.ServiceUnit, "err", err)
			return fmt.Errorf("systemctl start %s: %w", d.ServiceUnit, err)
		}

		if err := d.waitAlive(); err != nil {
			log.Error("gnb 启动后未就绪", "err", err)
			return err
		}
		log.Info("gnb 已就绪", "via", "systemd")
		return nil
	}
	bin := d.gnbBinary()
	pid, err := d.spawnGnb(bin)
	if err != nil {
		log.Error("gnb 启动失败", "bin", bin, "err", err)
		return fmt.Errorf("gnb 启动失败: %w", err)
	}
	d.writePidAfterSpawn(pid)

	if err := d.waitAlive(); err != nil {
		log.Error("gnb 启动后未就绪", "pid", pid, "err", err)
		return err
	}
	// 就绪后按 pid 文件校一次「唯一」：万一 pid 文件指向的是别人，这里会把它之外的多余实例收掉。
	if kept, perr := d.readPidFile(); perr == nil {
		if n := d.convergeSameConfdir(kept, "启动后校验唯一"); n > 0 {
			log.Warn("启动后发现多余同 confdir 实例并已收敛", "n", n, "keep", kept)
		}
	}
	log.Info("gnb 已就绪", "pid", pid, "bin", bin)
	return nil
}

// gnbBinary 解析 gnb 可执行文件。
// 优先级：Binary 字段 → $MYNET_GNB_BIN → PATH 上的 gnb[.exe] → 常见安装位。
// Windows 无 PATH 时必须能认 C:\gnb-win\bin\gnb.exe（183 实测形态）。
func (d *GNBDriver) gnbBinary() string {
	if d.Binary != "" {
		return d.Binary
	}
	return resolveGNBBinary()
}

// resolveGNBBinary 见 gnbBinary。与 plugin_proxy.findGnbBin / node_status.lookGnb 同一优先级：
//  1. $MYNET_GNB_BIN（显式覆盖）
//  2. <prefix>/bin/gnb[.exe]            ← 标准位（D4-E：driver/ 安装层退役）
//  3. PATH
//  4. 发行版常见安装位（含旧 $MYNET_HOME/driver/gnb/bin 等迁移期脚印）
//
// ⚠️ 不要一上来就 LookPath —— 系统 PATH 里可能是旧 gnb。
func resolveGNBBinary() string {
	name := gnbExeName()
	if v := os.Getenv("MYNET_GNB_BIN"); v != "" {
		return v
	}
	home := utils.GetMyNetHome()
	cands := []string{
		filepath.Join(utils.PrefixBinDir(), name),
		filepath.Join(utils.PrefixBinDir(), "gnb"),
		// 旧标准位（存量节点未迁移，只作查找回退，不再往这装）
		filepath.Join(home, "driver", "gnb", "bin", name),
		filepath.Join(home, "driver", "gnb", "bin", "gnb"),
		"/usr/local/opt/mynet/driver/gnb/bin/gnb",
		"/opt/mynet/driver/gnb/bin/gnb",
		"/usr/sbin/gnb",
		"/usr/local/opt/mynet/bin/gnb",
		"/opt/mynet/bin/gnb",
		// 历史手装位（183 迁移期）
		`C:\gnb-win\bin\gnb.exe`,
		`C:\gnb-win\gnb.exe`,
	}
	for _, p := range cands {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name // 交给 Spawn 去失败（错误信息里带名字）
}

// waitAlive 确认进程真的起来了。
//
// ⚠️ 「Spawn/systemctl 返回 0」都不代表起来了：
//   - 自我 daemon 化时父进程 fork 后立刻 exit(0)（gnb_daemon.c:40-42），
//     真正读配置建 tun 的是子进程，而子进程失败时 gnb 是 exit(0) 不是非零码
//     （gnb_keys.c:66,72,78 等），错误传不回父进程
//   - systemctl start 对 Type=simple 也是「fork 成功即返回」
//
// 所以必须回头探测。
func (d *GNBDriver) waitAlive() error {
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

	return fmt.Errorf("gnb 启动后 %s 内没有活进程；配置有误时 gnb 会直接退出且"+
		"错误报不回来，请检查 %s 下的 node.conf/route.conf/address.conf 与 security/ 密钥"+
		"（systemd 托管时另可看 journalctl -u %s）",
		time.Duration(probes)*delay, d.ConfDir, d.ServiceUnit)
}

// Stop 停隧道。systemd 托管时交给 systemctl —— 否则会与单元的 `Restart=always` 打架：
// 你 kill 掉它立刻又被拉起来，看起来像「停不掉」。
// 非托管时按 pid 文件发 SIGTERM，**不按名字**（AGENTS.md §3：多实例可同名并存，
// 禁 pkill/killall）。
//
// ⚠️ 此前是 `gnb_ctl -k`，而 gnb_ctl 的 getopt 串是 "b:n:csa:o46h"
// （gnb_ctl.c:102），long_options 里也没有 k —— **这个 flag 根本不存在**。
// gnb 装了 SIGTERM handler（gnb.c:395），发 SIGTERM 即可优雅退出。
func (d *GNBDriver) Stop() error { return d.withLifecycle(d.stopLocked) }

func (d *GNBDriver) stopLocked() error {
	log := logger.Comp("driver")
	if handled, err := delegatePlatformService(d.Exec, "stop"); handled {
		return err
	}
	log.Info("gnb 停止", "conf", d.ConfDir)
	if d.systemdManages() {
		if _, err := d.Exec("systemctl", "stop", d.ServiceUnit); err != nil {
			log.Error("systemctl stop 失败", "unit", d.ServiceUnit, "err", err)
			return fmt.Errorf("systemctl stop %s: %w", d.ServiceUnit, err)
		}
		return nil
	}
	pid, err := d.readPidFile()
	if err != nil {
		log.Warn("gnb 停止：读 pid 失败", "err", err)
		// 不直接返回：pid 文件坏了/被覆盖，仍有可能是同 confdir 的实例在跑 —— 交给收敛收。
		d.convergeSameConfdir(0, "pid 缺失时收敛")
		return err
	}
	if err := d.pidKillOS(pid); err != nil {
		log.Error("gnb 停止失败", "pid", pid, "err", err)
		return err
	}
	// 清 pid 文件：否则 node-status 会把「用户主动停」误判成
	// 「启动失败：gnb 已退出」（pid 文件在、进程没了）。
	_ = os.Remove(d.ConfDir + "/gnb.pid")
	// 兜底收敛：pid 文件只有一个 pid，被覆盖/指错时会有同 confdir 的残留。
	if n := d.convergeSameConfdir(0, "停止后清残留"); n > 0 {
		log.Info("停止后收敛了同 confdir 残留 gnb", "n", n)
	}
	log.Info("gnb 已停止", "pid", pid)
	return nil
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
//
// ⚠️ 两步必须在**同一把生命周期锁内**：分别调 Start/Stop 会各自取锁 —— 既可能自锁死，
// 也把「停完再起」这个原子语义拆开（那正是重叠 reload 留下两个 gnb 的形态）。
func (d *GNBDriver) Reload() error {
	return d.withLifecycle(func() error {
		// 平台服务管理器在 → 让它 restart（procd 自己串行化，且它才是实例的权威）。
		if handled, err := delegatePlatformService(d.Exec, "restart"); handled {
			return err
		}
		logger.Comp("driver").Info("gnb reload", "conf", d.ConfDir)
		_ = d.stopLocked()
		return d.startLocked()
	})
}

// lifecycleLockTimeout 等「另一个启停/重载」的最长时间；超时报错，不硬闯。
const lifecycleLockTimeout = 60 * time.Second

// withLifecycle 以**每 confdir 一把**的排他锁执行生命周期动作。
//
// 为什么必须有：驱动动作有多个发起方 —— mynetd 的 bundle_changed 事件链、CLI、LuCI 桥、
// 开机恢复 —— 它们各自 fork 出 `mynet service …` **子进程**，进程内互斥盖不住。
// 2026-10-11 生产 .180 实测：启动瞬间 `bundle_changed` 连跑 3 次 reload（事件层只合并掉
// 1 次），重叠的 Stop/Start 在同一 confdir 留下两个 gnb —— 一个持 tun、一个 tunless，
// 且 gnb.pid 记的恰是 tunless 那个。锁跨进程，正是为这一类。
func (d *GNBDriver) withLifecycle(fn func() error) error {
	rel, err := d.lockLifecycle()
	if err != nil {
		return err
	}
	defer rel()
	return fn()
}

// convergeSameConfdir 收掉「命令行指向本 ConfDir 的其它 gnb」，只留 keepPid（0 = 全清），
// 返回收掉的数量。
//
// 判据与 OpenWrt init 的 converge_stale_gnb 同源（**pid + cmdline**，绝不按名字杀），
// 但放在 Go 侧 ⇒ systemd / launchd / Windows 一并覆盖（#18 原先只在 OpenWrt init 上）。
// systemd 托管时不越权动它的进程 —— 单元自己保证唯一。
func (d *GNBDriver) convergeSameConfdir(keepPid int, why string) int {
	if d.systemdManages() {
		return 0
	}
	self := os.Getpid()
	n := 0
	for _, pid := range d.sameConfdirPidsOS() {
		if pid == keepPid || pid == self {
			continue
		}
		logger.Comp("driver").Info("收敛同 confdir 遗留 gnb", "pid", pid, "conf", d.ConfDir, "why", why)
		d.killUntilDead(pid)
		n++
	}
	return n
}

// killUntilDead 先 SIGTERM（gnb 装了 handler，可优雅退出），超时再硬杀。
func (d *GNBDriver) killUntilDead(pid int) {
	_ = d.pidKillOS(pid)
	for i := 0; i < 10; i++ {
		if !d.pidAliveOS(pid) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = d.pidKill9OS(pid)
}

// parseSameConfdirPids 从 `<pid> <argv...>` 行里挑出「命令行含 confDir 且 argv[0] 是 gnb」
// 的 pid。单独抽出来是为了可测：两种平台的扫描（unix ps / windows powershell）共用它，
// 而「误把只是提到该路径的别的进程当 gnb」是这条判据最容易出的错。
func parseSameConfdirPids(out, confDir, exeName string) []int {
	var pids []int
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, confDir) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		if filepath.Base(fields[1]) != exeName {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
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
	// Windows 不写 ifname（wintun 适配器名固定 P2PNet，见 gnb_platform_windows.go）
	if ifname := gnbIfname(g); ifname != "" && gnbPlatformConfKeys() == "" {
		fmt.Fprintf(&b, "ifname %s\n", ifname)
	}
	for _, ln := range g.Listeners {
		if ln = strings.TrimSpace(ln); ln != "" {
			fmt.Fprintf(&b, "listen %s\n", ln)
		}
	}
	b.WriteString(gnbDaemonLine())
	b.WriteString(gnbPlatformConfKeys())

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
		// proxy 的 gnb 开关（proxy on）**不在 node.conf**：归独立 proxy.conf
		// （driver/gnb/conf/<id>/proxy.conf）。此处不再渲染。
	}

	// zip auto：与线上节点压缩设置对齐。缺 zip 时大包会被对端压成我们解不开的帧，
	// 症状是「小 ping 通、≥~290B 全丢、SSH/HTTP 不通」（2026-09-27 实测）。
	// zip-level -1 = 默认级别；memory tiny = 小内存机型默认（gnb 历史包同款）。
	b.WriteString("zip auto\n")
	b.WriteString("zip-level -1\n")
	b.WriteString("forward on\n")
	b.WriteString("keepalive 25\n")

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
// ── 行的两种语义（gnb_conf_file.c:936-958，按 IP **末段** 分派）──
//
//	末段 ≠ 0 且该节点尚未设 tun IP → 主机行：设 tun 地址（每节点恰好一行）
//	末段 = 0，或该节点已有 tun IP  → 网段行：入 subnet ring（同节点可多行）
//
// 所以**不能按 nodeID 去重**：同一个 nodeid 完全可以是
//
//	1001|10.0.0.1|255.255.255.255   ← 主机行：节点自己的 tun
//	1001|192.168.0.0|255.255.255.0  ← 网段行：挂在它后面的 LAN
//
// 旧实现在这里用 seen[nodeID] 一节点只出一行，网段行被吞。
//
// ── 顺序约定（用户 2026-09-25）──
//
//	第一条 = 本机（g.Identity.NodeID），随后是对端主机行，最后是网段行按 nodeid 归组。
//	本机 nodeid 缺失时 gnb 直接 exit(1)（gnb_conf_file.c:1075-1078），
//	所以本机那条**必须**出现，置顶是为了人眼第一眼看到「我是谁」。
//
// ── 为什么节点一律 /32 ──
//
// 系统路由（apply）按 /32 精确装到「第一个节点的 gnb tun」上；
// 一台机跑 gnb1+gnb2 时，/32 保证互不淹没。网段行只用于节点**额外公告**的
// 身后子网，overlay 自身绝不用网段行。
func RenderGNBRouteConf(g *domain.GnbIntent) string {
	// 主机行：每节点至多一条（gnb 只认第一条设 tun IP）
	hostOf := make(map[string]hostLine, len(g.Nodes)+1)
	var hostOrder []string
	// 网段行：同节点可多条，键是 nodeID+网络地址，避免重复
	type netKey struct{ nodeID, ip, mask string }
	netSeen := make(map[netKey]bool)
	var nets []hostLine

	addHost := func(nodeID, cidr string) {
		if nodeID == "" || cidr == "" {
			return
		}
		if _, ok := hostOf[nodeID]; ok {
			return
		}
		ip, mask, ok := splitCIDR(cidr)
		if !ok {
			return
		}
		// gnb 用「IP 末段 ≠ 0」判主机行（gnb_conf_file.c:936-938 的 p[3]）。
		// 末段 = 0 的地址当主机行会被 gnb 当网段吃掉，tun 拿不到 IP。
		if !isRouteConfHostIP(ip) {
			return
		}
		hostOf[nodeID] = hostLine{nodeID: nodeID, ip: ip, mask: mask}
		hostOrder = append(hostOrder, nodeID)
	}

	addNet := func(nodeID, cidr string) {
		if nodeID == "" || cidr == "" {
			return
		}
		ip, mask, ok := splitCIDRNetwork(cidr)
		if !ok {
			return
		}
		// 网段行必须末段 = 0，否则 gnb 会把它当主机行候选
		if isRouteConfHostIP(ip) {
			return
		}
		k := netKey{nodeID, ip, mask}
		if netSeen[k] {
			return
		}
		netSeen[k] = true
		nets = append(nets, hostLine{nodeID: nodeID, ip: ip, mask: mask})
	}

	// ① Nodes.TunAddress —— 主机行主源（本机与对端都在这里）
	for _, n := range g.Nodes {
		addHost(n.NodeID, n.TunAddress)
	}
	// ② Routes：/32 归主机槽（节点尚无主机行时补上），其余归网段槽。
	//    projector 跳过自环，所以本机那条不可能从 Routes 来。
	for _, r := range g.Routes {
		if isHostPrefix(r.Prefix) {
			addHost(r.NodeID, r.Prefix)
		} else {
			addNet(r.NodeID, r.Prefix)
		}
	}

	// ③ 先按 nodeid 分组（同节点主机行+网段行相邻，主机行在前），
	//    再按 overlay IP 排序：本机置顶，其余同 /24 按 IP 升序、其它网段最后。
	local := g.Identity.NodeID

	type nodeGroup struct {
		id   string
		ip   string // 该节点主机行的 overlay，用于排序
		host hostLine
		nets []hostLine
	}
	groups := make([]*nodeGroup, 0, len(hostOf))
	byID := map[string]*nodeGroup{}

	get := func(id string) *nodeGroup {
		if g, ok := byID[id]; ok {
			return g
		}
		g := &nodeGroup{id: id}
		byID[id] = g
		groups = append(groups, g)
		return g
	}

	for _, id := range hostOrder {
		h := hostOf[id]
		g := get(id)
		g.host = h
		g.ip = h.ip
	}
	for _, n := range nets {
		g := get(n.nodeID)
		g.nets = append(g.nets, n)
	}

	// 本机第一；同网段（与本机同 /24）其次按 IP 升序；其它网段最后。
	// 这样 10.182.236.181 后面紧跟 182、183…，不会被 147.x 插队。
	localIP := ""
	if h, ok := hostOf[local]; ok {
		localIP = h.ip
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.id == local {
			return true
		}
		if b.id == local {
			return false
		}
		as, bs := sameSubnet24(a.ip, localIP), sameSubnet24(b.ip, localIP)
		if as != bs {
			return as // 同网段优先
		}
		return ipLess(a.ip, b.ip)
	})

	var b strings.Builder
	for _, g := range groups {
		if g.host.nodeID != "" {
			fmt.Fprintf(&b, "%s|%s|%s\n", g.host.nodeID, g.host.ip, g.host.mask)
		}
		for _, n := range g.nets {
			fmt.Fprintf(&b, "%s|%s|%s\n", n.nodeID, n.ip, n.mask)
		}
	}
	return b.String()
}

// sameSubnet24 判与本机是否同 /24。
func sameSubnet24(ip, local string) bool {
	a := net.ParseIP(ip).To4()
	l := net.ParseIP(local).To4()
	if a == nil || l == nil {
		return false
	}
	return a[0] == l[0] && a[1] == l[1] && a[2] == l[2]
}

// ipLess 按 IPv4 数值比较（空 IP 排后）。
func ipLess(a, b string) bool {
	pa := net.ParseIP(a).To4()
	pb := net.ParseIP(b).To4()
	if pa == nil {
		return false
	}
	if pb == nil {
		return true
	}
	for i := 0; i < 4; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

type hostLine struct{ nodeID, ip, mask string }

// isRouteConfHostIP 末段 ≠ 0 才是主机行（gnb_conf_file.c:936-938：`0 != p[3]`）。
func isRouteConfHostIP(ip string) bool {
	v := net.ParseIP(ip).To4()
	if v == nil {
		return false
	}
	return v[3] != 0
}

// isHostPrefix /32 或裸 IP 视为主机前缀。
func isHostPrefix(cidr string) bool {
	if _, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil {
		ones, bits := ipnet.Mask.Size()
		return ones == bits
	}
	return net.ParseIP(strings.TrimSpace(cidr)) != nil
}

// splitCIDRNetwork 取**网络地址**（IP & 掩码），网段行要用 192.168.0.0 而不是 192.168.0.5。
func splitCIDRNetwork(cidr string) (string, string, bool) {
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return "", "", false
	}
	if ip.To4() == nil {
		return "", "", false
	}
	net4 := ipnet.IP.To4()
	if net4 == nil {
		return "", "", false
	}
	mask := net.IP(ipnet.Mask).To4()
	if mask == nil {
		return "", "", false
	}
	return net4.String(), mask.String(), true
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
	sb.WriteString(gnbDaemonLine())
	sb.WriteString(gnbPlatformConfKeys())

	return sb.String()
}

// PeerLink 对端链路态（gnb_ctl -s）。
// Link 取值：direct / relay / local / offline / unknown
type PeerLink struct {
	NodeID    int64
	Overlay   string
	Link      string
	LatencyMs float64
}

// GnbPeerLinks 读 gnb_ctl 的对端链路表。key = nodeID。
//
// 供 localapi 的 node-status 用 —— UI 的「链路」列不能瞎猜，
// gnb_ctl 不可用时如实空表，前端显示 unknown。
//
// 判据复用 parseGNBStatus（格式对齐 gnb_ctl_dump.c）：
//   - `Local node`   → local
//   - `Direct`       → direct
//   - `InDirect`     → relay
//   - 其它/无 PONG   → offline
func GnbPeerLinks(confDir string) map[int64]PeerLink {
	d := NewGNBDriver(0)
	d.ConfDir = confDir
	// ctl 块是 gnb.map（不是 ctl），缺 -b 会打印 usage 却 exit 0
	out, err := d.Exec("gnb_ctl", "-b", confDir+"/gnb.map", "-c", "-s")
	if err != nil {
		return nil
	}
	if !strings.Contains(string(out), "node_num[") {
		return nil
	}
	peers := parseGNBStatus(string(out))
	if len(peers) == 0 {
		return nil
	}
	m := make(map[int64]PeerLink, len(peers))
	for _, p := range peers {
		link := linkFromStatus(p)
		m[p.NodeID] = PeerLink{
			NodeID:    p.NodeID,
			Overlay:   p.VirtualIP,
			Link:      link,
			LatencyMs: p.LatencyMs,
		}
	}
	return m
}

// linkFromStatus 把 VPNPeer 的 ConnectionStatus 映射成 UI 用的链路词。
//
// ⚠️ 两个坑（实测都踩过）：
//  1. markDirect 写的是 "online" 不是 "Direct" —— 只匹配 "Direct" 会把直连判成 offline
//  2. "InDirect" 包含子串 "Direct" —— 反序会把中继判成直连
func linkFromStatus(p *domain.VPNPeer) string {
	switch {
	case p.IsLocal, strings.Contains(p.ConnectionStatus, "Local"):
		return "local"
	case p.ConnectionStatus == "online",
		strings.Contains(p.ConnectionStatus, "Direct") && !strings.Contains(p.ConnectionStatus, "InDirect"):
		return "direct"
	case p.ConnectionStatus == "relay",
		strings.Contains(p.ConnectionStatus, "InDirect"),
		strings.Contains(p.ConnectionStatus, "Indirect"):
		return "relay"
	default:
		return "offline"
	}
}
