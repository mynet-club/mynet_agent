//go:build !windows

package mynet

// defaultPrefix unix 系统安装目录：linux/mac/bsd 统一 /opt/mynet。
//
// OpenWrt 特殊：二进制由 ipk 装到 /usr/sbin（见 openWrtPrefix 的说明），
// 所以这里先问平台钩子。
func defaultPrefix() string {
	if p := openWrtPrefix(); p != "" {
		return p
	}
	return "/opt/mynet"
}
