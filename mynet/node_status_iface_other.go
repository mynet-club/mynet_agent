//go:build !windows

package mynet

// 非 Windows 平台无 Get-NetAdapterStatistics；恒 false 走 sysfs/netstat 分支。
func windowsIfaceBytes(_ string) (rx, tx int64, mtu int, ok bool) {
	return 0, 0, 0, false
}
