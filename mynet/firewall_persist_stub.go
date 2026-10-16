//go:build !linux

package mynet

// darwin / windows 无「平台防火墙会清掉 mynet 规则」这条约束：
//   · darwin：pf 不归 mynet 托管，隧道侧规则由驱动与系统路由承担
//   · windows：wintun 侧另有约束，防火墙面不在本合同的解决范围
// 空实现保持调用面统一（上层合同仍在本包里，可单测）。
//
// 注意构建标签是 `!linux`（不是 `!openwrt`）：通用 Linux 有自己的执行面
// firewall_persist_linux.go。

func WriteFirewallInclude(home, tunDev string) error { return nil }

func SyncFirewallTunDevice(tunDev string) (bool, error) { return false, nil }

// LoadFirewallRules 这些平台不托管 nft/fw4：没有可委托的系统加载器。
func LoadFirewallRules(home string) error { return nil }

// currentFirewallBackend 这些平台不托管防火墙，无持久化面。
func currentFirewallBackend() FirewallBackend { return FirewallBackendNone }
