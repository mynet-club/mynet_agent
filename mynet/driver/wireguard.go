package driver

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/logger"
)

// WireGuardDriver 只读 + 生命周期。解析 `wg show`。
type WireGuardDriver struct {
	Interface string
	NodeID    int64
	ConfPath  string
	Exec      func(name string, args ...string) ([]byte, error)
}

// NewWireGuardDriver 不猜接口名。
//
// 曾经这里 `iface == "" → "wg0"`：接口名决定流量进哪条隧道，属于阻断性配置，
// 兜底出来的名字在多 zone 共存的机器上往往是**别人的接口**，而 wg 侧只要名字能
// 建起来就不报错。名字的权威是 bundle 的 node.interface，由 domain.BundleEnvelope.TunDevice()
// 解析并校验；解析不出来应当在调用方报错退出，而不是在这里造一个默认名。
func NewWireGuardDriver(iface string, nodeID int64) *WireGuardDriver {
	return &WireGuardDriver{
		Interface: iface,
		NodeID:    nodeID,
		Exec:      execCombined,
	}
}

func (d *WireGuardDriver) GetDriverType() domain.VPNDriverType {
	return domain.DriverTypeWireGuard
}

func (d *WireGuardDriver) IsAvailable() bool {
	_, err := d.Exec("wg", "show", d.Interface)
	return err == nil
}

func (d *WireGuardDriver) IsProcessAlive() bool { return d.IsAvailable() }

func (d *WireGuardDriver) Capabilities() domain.DriverCaps {
	// A-8：wg 无 Discovery / 无 Proxy。
	// HotReload=true：Reload 走 `wg syncconf`（只同步 WG 协议状态，不断链路），
	// 不可用时回落 Stop+Start（见 Reload）。
	return domain.DriverCaps{
		HotReload:       true,
		Discovery:       false,
		Proxy:           false,
		CustomKeyUpload: true,
	}
}

func (d *WireGuardDriver) Refresh() error {
	_, err := d.dump()
	return err
}

func (d *WireGuardDriver) GetAllPeers() ([]*domain.VPNPeer, error) {
	rows, err := d.dump()
	if err != nil {
		return nil, err
	}
	var peers []*domain.VPNPeer
	for _, r := range rows {
		if !r.isPeer {
			continue
		}
		status := "offline"
		if r.handshake {
			status = "online"
		}
		peers = append(peers, &domain.VPNPeer{
			PeerID:           r.peerNum,
			NodeID:           r.nodeID,
			VirtualIP:        strings.Join(r.allowedIPs, ","),
			ConnectionStatus: status,
			Endpoint:         r.endpoint,
			RxBytes:          r.rx,
			TxBytes:          r.tx,
			LastHandshake:    r.lastHS,
			AllowedIPs:       strings.Join(r.allowedIPs, ","),
		})
	}
	return peers, nil
}

func (d *WireGuardDriver) GetOnlinePeers() ([]*domain.VPNPeer, error) {
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

func (d *WireGuardDriver) GetPeer(peerID int64) (*domain.VPNPeer, error) {
	all, err := d.GetAllPeers()
	if err != nil {
		return nil, err
	}
	for _, p := range all {
		if p.PeerID == peerID {
			return p, nil
		}
	}
	return nil, fmt.Errorf("peer %d not found", peerID)
}

func (d *WireGuardDriver) GetLocalPeer() (*domain.VPNPeer, error) {
	return &domain.VPNPeer{NodeID: d.NodeID, IsLocal: true}, nil
}

func (d *WireGuardDriver) GetNetworkSummary() (*domain.VPNNetworkSummary, error) {
	peers, err := d.GetAllPeers()
	if err != nil {
		return nil, err
	}
	on, _ := d.GetOnlinePeers()
	var rx, tx uint64
	for _, p := range peers {
		rx += p.RxBytes
		tx += p.TxBytes
	}
	return &domain.VPNNetworkSummary{
		TotalPeers:     len(peers),
		OnlinePeers:    len(on),
		TotalRxBytes:   rx,
		TotalTxBytes:   tx,
		IsProcessAlive: d.IsProcessAlive(),
		LastUpdate:     time.Now(),
	}, nil
}

// WriteConfig 旧契约：VPNBundle → wg-quick 配置（keys.self + peers）。
// 新链路请用 WriteIntent（BundleEnvelope.WireGuardIntent）。
func (d *WireGuardDriver) WriteConfig(b *domain.VPNBundle) error {
	if b == nil {
		return fmt.Errorf("wireguard: nil bundle")
	}
	text := RenderWGConfigFromBundle(b)
	if d.ConfPath == "" {
		d.ConfPath = "/etc/wireguard/" + d.Interface + ".conf"
	}
	return writeFile0600(d.ConfPath, []byte(text))
}

// WriteIntent A-5：WireGuardIntent → wg-quick 配置（intent 内已是 wg base64）。
func (d *WireGuardDriver) WriteIntent(w *domain.WireGuardIntent) error {
	if w == nil {
		return fmt.Errorf("wireguard: nil intent")
	}
	text := RenderWGConfig(w)
	if d.ConfPath == "" {
		d.ConfPath = "/etc/wireguard/" + d.Interface + ".conf"
	}
	return writeFile0600(d.ConfPath, []byte(text))
}

func (d *WireGuardDriver) Start() error {
	_, err := d.Exec("wg-quick", "up", d.Interface)
	return err
}

func (d *WireGuardDriver) Stop() error {
	_, err := d.Exec("wg-quick", "down", d.Interface)
	return err
}

// wgQuickOnlyKeys 只属于 wg-quick、`wg setconf/syncconf` **不认**的键
// （wg-quick strip 干的就是剔掉它们；不剔 syncconf 直接语法错）。
// ⚠️ `FwMark`/`Fwmark` 是 **wg 自己的键**，不能剔。Address/MTU/DNS 归 L6（NetworkIntent）。
var wgQuickOnlyKeys = map[string]bool{
	"address": true, "mtu": true, "table": true, "dns": true,
	"preup": true, "postup": true, "predown": true, "postdown": true, "saveconfig": true,
}

// StripWGQuickKeys 把 wg-quick 配置剥成 `wg setconf/syncconf` 认的形态：
// 丢掉 wg-quick 专有键，保留 [Interface]/[Peer] 段与 wg 自己的键。
//
// 为什么自己剥而不用 `wg-quick strip`：那是 wg-quick 的子命令，而 **OpenWrt 没有
// wg-quick**（只有 wg）—— 用它等于把热同步钉死在有 wg-quick 的平台上。
func StripWGQuickKeys(text string) string {
	out := make([]string, 0, 16)
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
			out = append(out, line)
			continue
		}
		key := t
		if i := strings.IndexByte(t, '='); i >= 0 {
			key = strings.TrimSpace(t[:i])
		}
		if wgQuickOnlyKeys[strings.ToLower(key)] {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

// SyncFromConfFile 热同步：把盘上的配置剥成 wg 形态后 `wg syncconf <iface> <file>`。
//
// 只同步 **WG 协议状态**（私钥/端口/对端/AllowedIPs），**不含** Address/MTU/Table/
// FIB/rule/firewall —— 那些归 L6（driver-abstraction.md R7：两部分分开验证）。
// 接口不在时报错（syncconf 不能建口，先 up）。
func (d *WireGuardDriver) SyncFromConfFile() error {
	if _, err := d.Exec("wg", "show", d.Interface); err != nil {
		return fmt.Errorf("wireguard: 接口 %s 不在，不能 syncconf（应先 up）: %w", d.Interface, err)
	}
	raw, err := os.ReadFile(d.ConfPath)
	if err != nil {
		return fmt.Errorf("wireguard: 读 %s: %w", d.ConfPath, err)
	}
	tmp, err := os.CreateTemp("", "mynet-wg-sync-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(StripWGQuickKeys(string(raw))); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if out, err := d.Exec("wg", "syncconf", d.Interface, tmp.Name()); err != nil {
		return fmt.Errorf("wg syncconf %s: %w (%s)", d.Interface, err, strings.TrimSpace(string(out)))
	}

	return nil
}

// Reload 热重载优先：接口在跑 → `wg syncconf`（不断链路）；不在/失败 → 回落冷启 Stop+Start。
//
// 回落是契约的一部分（vpn_lifecycle_driver.go：HotReload 语义下 Reload 仍须能把状态带到位）；
// 但回落**要说话** —— 否则「syncconf 一直失败、一直偷偷冷启」会被当成热重载其实是通了。
func (d *WireGuardDriver) Reload() error {
	log := logger.Comp("driver")
	if err := d.SyncFromConfFile(); err == nil {
		log.Info("wireguard syncconf 热同步完成", "iface", d.Interface)
		return nil
	} else {
		log.Info("wireguard 热同步不可用，回落冷启 Stop+Start", "iface", d.Interface, "err", err)
	}
	_ = d.Stop()
	return d.Start()
}

func (d *WireGuardDriver) Status() (*domain.DriverStatus, error) {
	return &domain.DriverStatus{
		Running: d.IsAvailable(),
		NodeID:  strconv.FormatInt(d.NodeID, 10),
	}, nil
}

// ---- dump 解析 ----

type wgRow struct {
	isPeer     bool
	peerNum    int64
	nodeID     int64
	publicKey  string
	endpoint   string
	allowedIPs []string
	handshake  bool
	rx, tx     uint64
	lastHS     time.Time
	listenPort int
}

func (d *WireGuardDriver) dump() ([]wgRow, error) {
	out, err := d.Exec("wg", "show", d.Interface, "dump")
	if err != nil {
		return nil, fmt.Errorf("wg show dump: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return parseWGDump(string(out)), nil
}

func parseWGDump(s string) []wgRow {
	var rows []wgRow
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if i == 0 && len(f) >= 3 {
			// wg dump 接口行: private_key, public_key, listen_port, fwmark
			port, _ := strconv.Atoi(f[2])
			rows = append(rows, wgRow{listenPort: port, publicKey: f[1]})
			continue
		}
		if len(f) < 7 {
			continue
		}
		rx, _ := strconv.ParseUint(f[5], 10, 64)
		tx, _ := strconv.ParseUint(f[6], 10, 64)
		var hs time.Time
		ok := false
		if ts, err := strconv.ParseInt(f[4], 10, 64); err == nil && ts > 0 {
			hs = time.Unix(ts, 0)
			ok = time.Since(hs) < 2*time.Minute
		}
		var ips []string
		if f[3] != "" {
			ips = strings.Split(f[3], ",")
		}
		rows = append(rows, wgRow{
			isPeer: true, peerNum: int64(i), publicKey: f[0],
			allowedIPs: ips, handshake: ok,
			endpoint: f[2], rx: rx, tx: tx, lastHS: hs,
		})
	}
	return rows
}

// RenderWGConfig A-5：WireGuardIntent → wg-quick 文本。
func RenderWGConfig(w *domain.WireGuardIntent) string {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	b.WriteString("PrivateKey = " + w.Interface.PrivateKey + "\n")
	if w.Interface.ListenPort > 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", w.Interface.ListenPort)
	}
	// Table = off：proxy client 接管路由时，禁止 wg-quick 按 AllowedIPs 往主表写路由。
	// 否则目的集会被写进主表，连**本机自身**发往这些目的的流量一起卷进隧道
	// （spec/proxy-split.md §15.4 已下线的「主表 /32 钉扎」病）。模式值由 L5 投影设置。
	if w.Routing != nil && w.Routing.Mode == "off" {
		b.WriteString("Table = off\n")
	}
	for _, p := range w.Peers {
		b.WriteString("\n[Peer]\n")
		b.WriteString("PublicKey = " + p.PublicKey + "\n")
		if p.PresharedKey != nil && *p.PresharedKey != "" {
			b.WriteString("PresharedKey = " + *p.PresharedKey + "\n")
		}
		if p.BootstrapEndpoint != nil && *p.BootstrapEndpoint != "" {
			b.WriteString("Endpoint = " + *p.BootstrapEndpoint + "\n")
		}
		if len(p.AllowedIPs) > 0 {
			b.WriteString("AllowedIPs = " + strings.Join(p.AllowedIPs, ", ") + "\n")
		}
		if p.PersistentKeepaliveSecs > 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.PersistentKeepaliveSecs)
		}
	}
	return b.String()
}

// RenderWGConfigFromBundle 旧 VPNBundle 路径（keys.self + peers）。
func RenderWGConfigFromBundle(b *domain.VPNBundle) string {
	var sb strings.Builder
	sb.WriteString("[Interface]\n")
	sb.WriteString("PrivateKey = " + b.Keys.Self.Private + "\n")
	if b.Node.ListenPort > 0 {
		fmt.Fprintf(&sb, "ListenPort = %d\n", b.Node.ListenPort)
	}
	for _, p := range b.Peers {
		sb.WriteString("\n[Peer]\n")
		sb.WriteString("PublicKey = " + p.PublicKey + "\n")
		if p.Endpoint != "" {
			sb.WriteString("Endpoint = " + p.Endpoint + "\n")
		}
		if len(p.AllowedIPs) > 0 {
			sb.WriteString("AllowedIPs = " + strings.Join(p.AllowedIPs, ", ") + "\n")
		}
	}
	return sb.String()
}
