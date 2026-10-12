//go:build !linux && !windows

package mynet

// openWrtPrefix 非 Linux 平台没有 OpenWrt，恒空。
func openWrtPrefix() string { return "" }
