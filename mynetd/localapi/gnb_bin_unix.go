//go:build !windows

package localapi

// gnbExtraDirs 非 Windows 的 gnb 常见安装位（标准位 MYNET_HOME/driver/gnb/bin
// 由 lookGnb 统一拼，这里只补发行版路径）。
func gnbExtraDirs() []string {
	return []string{
		"/usr/local/opt/mynet/driver/gnb/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/opt/mynet/bin",
	}
}

// gnbExeName 平台可执行名。
func gnbExeName() string { return "gnb" }
