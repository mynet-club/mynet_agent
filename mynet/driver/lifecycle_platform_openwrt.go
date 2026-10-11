//go:build linux && openwrt

package driver

import "os"

// procdInitPath OpenWrt 的服务单元（由 `mynet install` 从内嵌模板写入）。
const procdInitPath = "/etc/init.d/mynet"

// platformServiceOwner OpenWrt（procd）上由 `/etc/init.d/mynet` 拥有隧道生命周期
// —— init 里按 `VPN_TYPE` 分支：`gnb → start_gnb_instance`（procd 实例）、
// `wireguard → start_wg_interface`（有 `ip`+`wg` 兜底，不依赖 wg-quick）。
// 与 Lua 执行器委托的**同一个脚本、同一条规则**。
func platformServiceOwner() string {
	if st, err := os.Stat(procdInitPath); err == nil && !st.IsDir() {
		return procdInitPath
	}

	return ""
}
