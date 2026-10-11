//go:build !linux || !openwrt

package driver

// platformServiceOwner 非 OpenWrt 构建：没有 procd 这一层。
//
// systemd 托管由驱动自己的 `systemdManages()` 判（走 systemctl）；darwin/Windows 分别由
// launchd / 服务自管 —— 都不在这里再叠一层。
func platformServiceOwner() string { return "" }
