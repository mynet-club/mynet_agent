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

// parseGNBStatus 判据（E-3）：PONG / online——不用已下架的 mynet_proxy 状态。
func parseGNBStatus(out string) []*domain.VPNPeer {
	var peers []*domain.VPNPeer
	n := int64(0)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		n++
		status := "offline"
		if strings.Contains(line, "PONG") || strings.Contains(line, "online") {
			status = "online"
		}
		peers = append(peers, &domain.VPNPeer{PeerID: n, ConnectionStatus: status})
	}
	return peers
}

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
