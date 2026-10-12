//go:build !windows

package mynet

import "runtime"

// defaultPrefix unix 系统安装目录。
//
// OpenWrt 特殊：二进制由 ipk 装到 /usr/sbin，系统里没有 /opt/mynet
// （见 openWrtPrefix 的说明），所以这里先问平台钩子。
func defaultPrefix() string {
	if runtime.GOOS == "darwin" {
		return "/usr/local/opt/mynet"
	}
	if p := openWrtPrefix(); p != "" {
		return p
	}
	return "/opt/mynet"
}
