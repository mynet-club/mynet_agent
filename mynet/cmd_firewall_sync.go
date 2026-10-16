package mynet

// mynet firewall sync —— 防火墙持久化同步（上层合同见 firewall_persist_common.go）。
//
//   mynet firewall sync
//
// 两件事（幂等）：
//  1. 落地上层产物 `$MYNET_HOME/scripts/firewall.mynet.include`，并按平台登记
//     持久化面 —— OpenWrt 走 UCI mynet_include（fw4 reload 后重放 NAT 规则），
//     通用 Linux 按探测到的后端登记（nftables.d / netfilter-persistent；都没有
//     就明确报错，不假装成功）。
//  2. 让持久化产物跟上**当前** tun 名（bundle node.interface）；OpenWrt 侧还把
//     network.mynet / zone mynet 的 device 对齐到它。
//
// 触发点：`apply --auto` 尾部、`service start` 隧道接口就绪之后、install。
// **不是 mynetd 启动时** —— mynetd 是纯通信层，系统能力归执行器（AGENTS.md §1.2）。

import (
	"errors"
	"fmt"
	"os"

	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// syncFirewallAfterTunnelUp 隧道接口建立后同步防火墙持久化。
//
// 位置语义：必须在**隧道接口确实起来之后**调用 —— 产物里的 DEV 要对上真实
// 接口名，接口没起来时算出来的名字可能与实际投影不一致。
//
// 失败只告警不阻断：apply 本体已装好路由，防火墙同步是收尾；但告警要指因
// （丢了持久化 = 防火墙一重启规则就没了）。
func syncFirewallAfterTunnelUp() {
	home := utils.GetMyNetHome()
	dev := currentTunDeviceFromBundle()
	if err := WriteFirewallInclude(home, dev); err != nil {
		if errors.Is(err, ErrNoPersistenceSurface) {
			// 只丢「防火墙重启后自动重放」这一项；**产物已写、当次照装**。
			// 登记不了 ≠ 当次失败，但必须说出来（skip ≠ pass）。
			logger.Comp("apply").Warn("防火墙无持久化面：重启后不会自动重放，需手动 `mynet firewall sync`", "err", err)
		} else {
			logger.Comp("apply").Warn("防火墙产物写入失败", "dev", dev, "err", err)
			return
		}
	}
	// 写完产物**委托系统自己的加载器装** —— Go 不 exec `nft add rule`（规则本体不进 Go）。
	if err := LoadFirewallRules(home); err != nil {
		logger.Comp("apply").Warn("防火墙规则加载失败（系统无可用加载器或产物缺失）", "err", err)
		return
	}
	if dev == "" {
		return
	}
	changed, err := SyncFirewallTunDevice(dev)
	if err != nil {
		logger.Comp("apply").Warn("防火墙接口绑定同步失败", "dev", dev, "err", err)
		return
	}
	if changed {
		logger.Comp("apply").Info("防火墙接口绑定已对齐", "dev", dev)
	}
}

// currentTunDeviceFromBundle 从 bundle 取 tun 接口名（唯一解析口 TunDevice）。
// 取不到返回空 —— 上层会只出骨架，等下一次 sync 补上。
func currentTunDeviceFromBundle() string {
	env, err := loadBundleEnvelope()
	if err != nil {
		return ""
	}
	dev, err := env.TunDevice()
	if err != nil {
		return ""
	}
	return dev
}

// resolveListenPort 读 bundle 的 node.listen_port（firewallListenPortResolver 默认实现）。
// 取不到返回 0 —— 不猜端口（猜错会放行错误的口，比不放行更危险）。
func resolveListenPort() int {
	env, err := loadBundleEnvelope()
	if err != nil || env == nil {
		return 0
	}
	return env.Node.ListenPort
}

// resolveGatewayIntent 读 bundle 的 network.firewall，回答本节点是否网关
// （firewallGatewayResolver 的默认实现）。
//
// 云端只发**意图**（BundleEnvelopePublisher::firewallIntent，判据 Node::isGateway），
// Go 侧不实现防火墙语义，只据此决定**产物里要不要有转发规则**。
func resolveGatewayIntent() bool {
	env, err := loadBundleEnvelope()
	if err != nil || env == nil {
		return false
	}
	for _, raw := range env.Network.Firewall {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if act, _ := m["action"].(string); act == "ensure-masq" {
			return true
		}
	}
	return false
}

// resolveDefaultWANDevice 解析**本机**默认出接口（firewallWANResolver 的默认实现）。
// 只在产物生成入口问一次系统；纯核里（FirewallReplayRules 等）一律显式传参可测。
func resolveDefaultWANDevice() string {
	snap, err := shared.ReadSnapshot(shared.DefaultSnapshotPath())
	if err != nil {
		return ""
	}
	return defaultIfaceFromSnapshot(snap)
}

func cmdFirewallSync(args []string) int {
	home, code := requireHome()
	if code != 0 {
		return code
	}
	dev := currentTunDeviceFromBundle()
	if dev == "" {
		fmt.Fprintln(os.Stderr, "firewall sync: bundle 里解析不到接口名（先 mynet config pull）—— 产物只出骨架")
	}
	if err := WriteFirewallInclude(home, dev); err != nil {
		if errors.Is(err, ErrNoPersistenceSurface) {
			// 哨兵：登记不了持久化面，但产物已写、当次照装 —— 告警后继续。
			fmt.Fprintf(os.Stderr, "⚠ %v\n", err)
			fmt.Fprintln(os.Stderr, "  可选修复：安装 iptables-persistent；或让 /etc/nftables.conf include /etc/nftables.d/")
		} else {
			fmt.Fprintf(os.Stderr, "firewall sync: %v\n", err)
			return 1
		}
	}
	fmt.Println("✓ include:", FirewallIncludePath(home))
	fmt.Println("  持久化面:", currentFirewallBackend())
	if err := LoadFirewallRules(home); err != nil {
		fmt.Fprintf(os.Stderr, "firewall sync: 加载失败 —— %v\n", err)
		return 1
	}
	fmt.Println("✓ 已委托系统加载")
	if dev == "" {
		return 0
	}
	changed, err := SyncFirewallTunDevice(dev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "firewall sync: %v\n", err)
		return 1
	}
	if changed {
		fmt.Println("✓ 接口绑定已对齐", dev)
	} else {
		fmt.Println("✓ 接口绑定已一致:", dev)
	}
	return 0
}
