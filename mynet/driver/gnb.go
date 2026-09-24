package driver

import (
	"fmt"
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
}

func NewGNBDriver(nodeID int64) *GNBDriver {
	return &GNBDriver{
		NodeID:  nodeID,
		ConfDir: fmt.Sprintf("driver/gnb/conf/%d", nodeID),
		Exec:    execCombined,
	}
}

func (d *GNBDriver) GetDriverType() domain.VPNDriverType { return domain.DriverTypeGNB }

func (d *GNBDriver) IsAvailable() bool {
	_, err := d.Exec("gnb_ctl", "-s")
	return err == nil
}

func (d *GNBDriver) IsProcessAlive() bool {
	out, err := d.Exec("pgrep", "-f", "gnb_es")
	_ = out
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
	_, err := d.Exec("gnb_ctl", "-s")
	return err
}

func (d *GNBDriver) GetAllPeers() ([]*domain.VPNPeer, error) {
	out, err := d.Exec("gnb_ctl", "-s")
	if err != nil {
		return nil, fmt.Errorf("gnb_ctl -s: %w", err)
	}
	return parseGNBStatus(string(out)), nil
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

// WriteIntent GnbIntent → node.conf / route.conf / address.conf。
// ⚠️ 掩码只认 /32 /24 /16 /8 /0——其余静默忽略（驱动事实）。
func (d *GNBDriver) WriteIntent(g *domain.GnbIntent) error {
	if g == nil {
		return fmt.Errorf("gnb: nil intent")
	}
	if err := writeFile0600(d.ConfDir+"/node.conf", []byte(RenderGNBNodeConf(g))); err != nil {
		return err
	}
	if len(g.Routes) > 0 {
		var b strings.Builder
		for _, r := range g.Routes {
			// GnbRoute：node_id + prefix（CIDR）；via/proxy 由转发策略另落
			fmt.Fprintf(&b, "%s %s\n", r.NodeID, r.Prefix)
		}
		_ = writeFile(d.ConfDir+"/route.conf", []byte(b.String()), 0o644)
	}
	return nil
}

func (d *GNBDriver) Start() error {
	_, err := d.Exec("gnb_ctl", "-b", d.ConfDir+"/gnb.map")
	return err
}

func (d *GNBDriver) Stop() error {
	_, err := d.Exec("gnb_ctl", "-k")
	return err
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

// RenderGNBNodeConf GnbIntent → node.conf（服务端渲染语义）。
func RenderGNBNodeConf(g *domain.GnbIntent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "node %s\n", g.Identity.NodeID)
	for _, ln := range g.Listeners {
		fmt.Fprintf(&b, "listen %s\n", ln)
	}
	// 交给 procd 前台（E-2）
	b.WriteString("daemon off\n")
	return b.String()
}

func RenderGNBNodeConfFromBundle(b *domain.VPNBundle) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "node %s\n", b.Node.ID)
	if b.Node.ListenPort > 0 {
		fmt.Fprintf(&sb, "listen %d\n", b.Node.ListenPort)
	}
	sb.WriteString("daemon off\n")
	return sb.String()
}
