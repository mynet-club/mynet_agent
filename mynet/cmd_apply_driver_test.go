package mynet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/mynet/driver"
	"github.com/mynet-club/mynet_agent/shared/domain"
)

// writeBundleAtHome 把 envelope 落到 $MYNET_HOME/var/bundle.json ——
// mynetd 拉包、mynet service、apply --auto 读的是同一个文件。
func writeBundleAtHome(t *testing.T, env *domain.BundleEnvelope) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "var"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "var", "bundle.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	return home
}

func wgEnv(ifname string) *domain.BundleEnvelope {
	return &domain.BundleEnvelope{
		Node:   domain.NodeRef{ID: "5063883630803570", Interface: ifname},
		Driver: domain.DriverTypeWireGuard,
	}
}

// TestLifecycleDriverWireGuardUsesResolvedDevice wg 的接口名与 ConfPath 同源。
//
// wg-quick 只认 /etc/wireguard/<iface>.conf，名字取错就是「起了另一条隧道」。
// 此前这里读 env.Node.Interface 并给空值兜底 wg0 —— 绕过 TunDevice() 的校验，
// 而接口名是阻断性配置：兜底出来的名字在多台 zone 共存的机器上往往是别人的接口。
func TestLifecycleDriverWireGuardUsesResolvedDevice(t *testing.T) {
	writeBundleAtHome(t, wgEnv("wg_home_2"))

	d, err := buildLifecycleDriver(domain.DriverTypeWireGuard)
	if err != nil {
		t.Fatal(err)
	}
	wg, ok := d.(*driver.WireGuardDriver)
	if !ok {
		t.Fatalf("驱动类型 %T", d)
	}
	if wg.Interface != "wg_home_2" {
		t.Fatalf("Interface=%q，应取 node.interface", wg.Interface)
	}
	if wg.ConfPath != "/etc/wireguard/wg_home_2.conf" {
		t.Fatalf("ConfPath=%q，必须与接口名同源", wg.ConfPath)
	}
}

// TestLifecycleDriverWireGuardRefusesMissingDevice 名字解析不出来就报错，不兜底 wg0。
func TestLifecycleDriverWireGuardRefusesMissingDevice(t *testing.T) {
	writeBundleAtHome(t, wgEnv(""))

	if _, err := buildLifecycleDriver(domain.DriverTypeWireGuard); err == nil {
		t.Fatal("接口名缺失时必须报错，不许退回 wg0")
	} else if !strings.Contains(err.Error(), "接口名") {
		t.Fatalf("错误应说明是接口名的问题: %v", err)
	}
}

// TestLifecycleDriverWireGuardRejectsForeignNamePattern 权威来自 gnb 别名之外的字段时
// 同样要过 IFNAMSIZ/字符集校验：wg 侧的 `ip link add dev X` 与 ConfPath 拼接
// 都不校验长度，超长的名字会静默变成另一个接口。
func TestLifecycleDriverWireGuardRejectsForeignNamePattern(t *testing.T) {
	writeBundleAtHome(t, wgEnv("wg_home;rm -rf /etc"))

	if _, err := buildLifecycleDriver(domain.DriverTypeWireGuard); err == nil {
		t.Fatal("非法接口名不该构造出驱动")
	}
}
