//go:build linux

package mynet

import "os"

// isOpenWrt — 运行期判定 OpenWrt（不依赖 runtime.GOOS：OpenWrt 也是 linux）。
//
// 放在共享的 linux 文件里，是因为**通用 Linux 后端也需要它**：探测到 OpenWrt 时要
// 明确拒绝装配（见 cmd_install_systemd.go），而不是夹带一套 OpenWrt 逻辑。
// OpenWrt 的目录（/etc/mynet、/sbin）、服务（procd）、防火墙（fw4/UCI zone）与
// 通用 Linux（systemd、iptables/nft、/opt/mynet）差异很大，装配路径不共享。
func isOpenWrt() bool {
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return true
	}
	// 兜底：procd 存在且无 systemd（ImmortalWrt 同口径）
	if _, err := os.Stat("/sbin/procd"); err == nil {
		if _, err := os.Stat("/etc/systemd/system"); err != nil {
			return true
		}
	}
	return false
}
