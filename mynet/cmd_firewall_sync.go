package mynet

// mynet firewall sync —— OpenWrt 防火墙绑定/持久化同步。
//
//   mynet firewall sync
//
// 两件事（幂等）：
//  1. 重写 $MYNET_HOME/scripts/firewall.mynet.include（fw4 reload 后重放 NAT 规则
//     —— 持久化机制必须保留）+ UCI include 登记。
//  2. network.mynet / zone mynet 的 device 对齐**当前** tun 名（bundle
//     node.interface）；变了才写 + firewall reload。
//
// apply --auto 尾部与 install 也走同一函数；非 OpenWrt 为空实现。

import (
	"fmt"
	"os"

	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// syncFirewallAfterApply apply 成功后同步防火墙持久化/绑定。
// 失败只告警不阻断 —— apply 本体已装好路由，防火墙同步是收尾；
// 但告警要指因（丢了持久化 = fw4 reload 后规则没了）。
func syncFirewallAfterApply() {
	home := utils.GetMyNetHome()
	dev := ""
	if env, err := loadBundleEnvelope(); err == nil {
		if d, derr := env.TunDevice(); derr == nil {
			dev = d
		}
	}
	if err := WriteFirewallInclude(home, dev); err != nil {
		logger.Comp("apply").Warn("firewall include 同步失败（fw4 reload 会丢 NAT 规则）", "err", err)
		return
	}
	if dev == "" {
		return
	}
	changed, err := SyncFirewallTunDevice(dev)
	if err != nil {
		logger.Comp("apply").Warn("防火墙 UCI device 同步失败", "dev", dev, "err", err)
		return
	}
	if changed {
		logger.Comp("apply").Info("UCI device 对齐 + firewall reload", "dev", dev)
	}
}

func cmdFirewallSync(args []string) int {
	home, code := requireHome()
	if code != 0 {
		return code
	}
	dev := ""
	if env, err := loadBundleEnvelope(); err == nil {
		if d, derr := env.TunDevice(); derr == nil {
			dev = d
		}
	}
	if dev == "" {
		fmt.Fprintln(os.Stderr, "firewall sync: bundle 里解析不到接口名（先 mynet config pull）—— include 只出骨架")
	}
	if err := WriteFirewallInclude(home, dev); err != nil {
		fmt.Fprintf(os.Stderr, "firewall sync: %v\n", err)
		return 1
	}
	fmt.Println("✓ include:", FirewallIncludePath(home))
	if dev == "" {
		return 0
	}
	changed, err := SyncFirewallTunDevice(dev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "firewall sync: %v\n", err)
		return 1
	}
	if changed {
		fmt.Println("✓ UCI device 对齐", dev, "（network.mynet + zone mynet）+ firewall reload")
	} else {
		fmt.Println("✓ UCI device 已一致:", dev)
	}
	return 0
}
