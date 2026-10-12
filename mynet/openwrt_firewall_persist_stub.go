//go:build !openwrt

package mynet

// 非 OpenWrt 平台无 UCI/fw4：防火墙持久化不适用（systemd/launchd 主机的
// 防火墙不归 mynet 托管；Windows 有 wintun 侧约束）。空实现保持调用面统一。

func WriteFirewallInclude(home, tunDev string) error { return nil }

func SyncFirewallTunDevice(tunDev string) (bool, error) { return false, nil }
