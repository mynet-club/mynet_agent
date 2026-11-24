//go:build !linux

package driver

// platformServiceOwner 非 Linux 构建：没有 OpenWrt procd 这一层。
//
// darwin/Windows 分别由 launchd / 服务自管 —— 不在这里再叠一层。
func platformServiceOwner() string { return "" }
