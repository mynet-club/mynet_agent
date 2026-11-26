//go:build !openwrt

package mynet

// 非 OpenWrt 平台没有 fw3 include / uci / iptables 这套执行面：
// 两跳中转资产为空实现（上层派生的 relayACL 仍可单测）。
func EnsureRelayAssets(home string, acl relayACL) error { return nil }
