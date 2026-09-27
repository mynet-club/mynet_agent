//go:build !windows

package mynet

import "runtime"

// defaultPrefix unix 系统安装目录。
func defaultPrefix() string {
	if runtime.GOOS == "darwin" {
		return "/usr/local/opt/mynet"
	}
	return "/opt/mynet"
}
