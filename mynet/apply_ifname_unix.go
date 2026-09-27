//go:build !windows

package mynet

// platformIfaceToken netsh 等工具吃的名字。unix 直接用接口名。
func platformIfaceToken(name string) string { return name }

// platformTunnelIface 逻辑名解析失败时的兜底。unix 原样返回。
func platformTunnelIface(name string) string { return name }
