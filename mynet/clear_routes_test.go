package mynet

import (
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// TestManagedTunnelDeviceUsesBundleTunOnly 清理目标只能来自 node.interface。
//
// 现网反例（.178）：mynet 的 gnb_tun 与手工 wg_arthur（10.66.66.12）并存。
// 旧实现按 utun/tun/gnb/wg 前缀枚举，stop 时把 wg_arthur 上的回程路由一并删了。
func TestManagedTunnelDeviceUsesBundleTunOnly(t *testing.T) {
	env := planEnv("gnb_tun_16", "gnb_tun_16")
	env.Network.Addresses = nil // 不依赖执行机恰好存在同名口

	dev, err := managedTunnelDevice(env)
	if err != nil {
		t.Fatal(err)
	}
	if dev != "gnb_tun_16" {
		t.Fatalf("受管口 = %q，应取 node.interface", dev)
	}
	if strings.HasPrefix(dev, "wg") || strings.HasPrefix(dev, "utun") {
		t.Fatalf("受管口解析成了别的隧道: %q", dev)
	}
}

// TestManagedTunnelDeviceRefusesUnknown 接口名缺失或两处冲突时必须报错：
// 宁可不清，也不按名字猜一个口。
func TestManagedTunnelDeviceRefusesUnknown(t *testing.T) {
	env := &domain.BundleEnvelope{Node: domain.NodeRef{ID: "1"}}
	if _, err := managedTunnelDevice(env); err == nil {
		t.Fatal("无接口名时不该给出清理目标")
	}

	env = planEnv("gnb_tun_16", "gnb_tun_16")
	env.Gnb.Options.Ifname = "gnb_tun"
	if _, err := managedTunnelDevice(env); err == nil {
		t.Fatal("接口名冲突时不该给出清理目标")
	}
}

// TestClearTunnelRoutesRefusesWithoutBundle 读不到 bundle 时跳过清理并报非零，
// 不能退回「枚举所有像隧道的接口」那种旧行为。
func TestClearTunnelRoutesRefusesWithoutBundle(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	if code := clearTunnelRoutes(snapIP(), true); code == 0 {
		t.Fatal("bundle 缺失时 clear 不该假装成功")
	}
}
