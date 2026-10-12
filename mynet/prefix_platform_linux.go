//go:build linux

package mynet

// openWrtPrefix OpenWrt 的安装前缀。
//
// OpenWrt（含 ImmortalWrt）上二进制由 ipk 装到 **/usr/sbin**（OpenWrt 惯例），
// 系统里根本没有 /opt/mynet —— 沿用 Linux 默认前缀会让 `mynet install` 去
// /opt/mynet/sbin 找二进制、然后报「缺二进制」而拒绝装服务（.178 实测）。
// 故 OpenWrt 一律用 /usr 作前缀（binDir = /usr/sbin，与 ipk 对齐）。
func openWrtPrefix() string {
	if isOpenWrt() {
		return "/usr"
	}
	return ""
}
