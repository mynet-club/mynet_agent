package mynet

import (
	"os/exec"
	"strings"

	"github.com/mynet-club/mynet_agent-V2/mynet/driver"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
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
		return nil
	}
	d := driver.NewWireGuardDriver(iface, 0)
	peers, err := d.GetAllPeers()
	if err != nil {
		// 接口在但读不到 peer（例如非 root 时 `wg show <iface> dump` 会
		// Permission denied，本机实测就是这样）。这是**降级**不是错误：
		// 宁可少报，也不要让 `mynet metrics` 因为权限问题整体失败。
		return nil
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

func probeGNB() []VPNDriverMetrics {
	d := driver.NewGNBDriver(0)
	if !d.IsAvailable() {
		return nil
	}
	peers, err := d.GetAllPeers()
	if err != nil {
		return nil
	}
	return []VPNDriverMetrics{projectPeers("gnb", "", peers)}
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
		for _, a := range []string{p.VirtualIP, p.VirtualIP6} {
			if a != "" && !seen[a] {
				seen[a] = true
				m.Addrs = append(m.Addrs, a)
			}
		}
	}
	return m
}
