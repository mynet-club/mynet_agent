package mynet

// tun_health —— 隧道健康信号（G3 缺口⑥）。
//
// 现象（180/183 实证）：tun 名被占/创建失败时 gnb 进程活着但零流量，
// 服务是 "active"、状态面却没有信号 —— 「起没起」与「隧道可用」是两件事。
//
// 判据（全部复用现成件，不造新机制）：
//   - VPN IP 来源：route.conf self 行 → 意图（overlayFromRouteConf / readIntentOverlay）
//   - 挂没挂 IP：ifaceWithAddr（按 IP 反查接口，darwin utunN / linux gnb_tun 都认）
//   - 期望口名：bundle 的 node.interface（后端权威 ifname）—— 只作信息位，
//     不作健康判据：darwin 上配置名（gnb_tun）与真实口（utunN）本就不同名。
//
// 健康判据是**平台中立**的「VPN IP 挂没挂」：挂上即隧道可用，挂不上即零流量。

import (
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// TunHealth 隧道健康信号。tun_exists / vpn_ip_attached 两个事实分列，
// Checked=false 表示无判据（route.conf/意图无 self 行且 bundle 无口名）——
// 不瞎判，调用方只提示不拦截。
type TunHealth struct {
	VPNIP         string `json:"vpn_ip,omitempty"`
	ExpectedIface string `json:"expected_iface,omitempty"`
	TunIface      string `json:"tun_iface,omitempty"`
	TunExists     bool   `json:"tun_exists"`
	VpnIPAttached bool   `json:"vpn_ip_attached"`
	Checked       bool   `json:"checked"`
}

// Healthy 隧道可用 = VPN IP 挂上了（平台中立判据）。
// VPN IP 未知时退化看期望口在不在（仅 Linux 有意义），都未知即不可判。
func (h TunHealth) Healthy() bool {
	if h.VPNIP != "" {
		return h.VpnIPAttached
	}
	return h.ExpectedIface != "" && h.TunExists
}

// Summary 一言之（`mynet status` 等状态面用）。
func (h TunHealth) Summary() string {
	switch {
	case !h.Checked:
		return "? 无判据（route.conf/意图无 self 行，bundle 无口名）"
	case h.Healthy():
		return fmt.Sprintf("✓ up · %s · VPN IP %s 已挂", orDash(h.TunIface), h.VPNIP)
	default:
		return fmt.Sprintf("✗ tun 未起 —— VPN IP %s 无接口承载（预期口 %s），疑名字被占/权限不足",
			orDash(h.VPNIP), orDash(h.ExpectedIface))
	}
}

// computeTunHealth 健康判据本体（纯函数，测试注入替身）。
// findIface 按 IP 反查接口（ifaceWithAddr）；ifaceExists 按名查口（可空）。
func computeTunHealth(vpnIP, expectedIface string, findIface func(string) string, ifaceExists func(string) bool) TunHealth {
	h := TunHealth{
		VPNIP:         vpnIP,
		ExpectedIface: expectedIface,
		Checked:       vpnIP != "" || expectedIface != "",
	}
	if vpnIP != "" && findIface != nil {
		h.TunIface = findIface(vpnIP)
		h.VpnIPAttached = h.TunIface != ""
	}
	switch {
	case expectedIface != "" && ifaceExists != nil:
		h.TunExists = ifaceExists(expectedIface)
	default:
		h.TunExists = h.VpnIPAttached
	}
	return h
}

// ifaceExistsByName 按接口名查存在（标准库、跨平台、不 exec、不要 root）。
func ifaceExistsByName(name string) bool {
	if name == "" {
		return false
	}
	_, err := net.InterfaceByName(name)
	return err == nil
}

// tunNameFromBundle bundle 的 node.interface（权威 ifname）+ 主驱动。
// 解析不出返回 ok=false（宁缺不猜，同 gnbIfnameFromBundle 的口径）。
func tunNameFromBundle() (name string, drv domain.VPNDriverType, ok bool) {
	env, err := loadBundleEnvelope()
	if err != nil || env == nil {
		return "", "", false
	}
	name, derr := env.TunDevice()
	if derr != nil {
		return "", "", false
	}
	return name, env.Driver, true
}

// currentTunHealth 读真环境算一次（status 面 / 启动验收共用）。
// VPN IP：route.conf self 行 → 意图；期望口：bundle node.interface。
func currentTunHealth(home string) TunHealth {
	vpnIP := overlayFromRouteConf(filepath.Join(gnbProjectionDir(home), "route.conf"))
	if vpnIP == "" {
		vpnIP = readIntentOverlay(home)
	}
	expected, _, _ := tunNameFromBundle()
	return computeTunHealth(vpnIP, expected, ifaceWithAddr, ifaceExistsByName)
}

// tunStartAcceptance 启动验收判据（缺口⑥）：gnb 活了但 tun 未起 → error（fail loud）。
//
// 只按 VPN IP 判（ifaceWithAddr）：期望口名跨平台不可靠（darwin 配置名≠真实口），
// 不能拿它拦启动。VPN IP 未知（route.conf/意图都没有 self 行）时不拦 ——
// 无判据不等于失败，只提示。
func tunStartAcceptance(h TunHealth, driverName string) error {
	if h.VPNIP == "" {
		return nil
	}
	if h.VpnIPAttached {
		return nil
	}
	return fmt.Errorf("%s 在跑但 tun 未起，疑名字被占/权限不足：VPN IP %s 无接口承载（预期口 %s，按 IP 反查无接口）",
		driverName, h.VPNIP, orDash(h.ExpectedIface))
}

// verifyTunAfterStart 启动验收（waitAlive 之后）：给 tun 短暂收敛的时间，
// 仍不健康才 fail loud（返回 error，调用方非零退出）。
// probe 可注入（测试打桩）；tries<=0 视作 1。
func verifyTunAfterStart(driverName string, probe func() TunHealth, tries int, delay time.Duration) error {
	if probe == nil {
		return nil
	}
	if tries <= 0 {
		tries = 1
	}
	var h TunHealth
	for i := 0; i < tries; i++ {
		h = probe()
		if h.VPNIP == "" || h.VpnIPAttached {
			return tunStartAcceptance(h, driverName) // 健康或无判据 → nil
		}
		if i < tries-1 && delay > 0 {
			time.Sleep(delay)
		}
	}
	return tunStartAcceptance(h, driverName)
}
