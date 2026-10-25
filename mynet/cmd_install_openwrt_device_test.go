//go:build linux && openwrt

package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 安装期的接口名：只认 bundle，猜不出来就不绑。
//
// 此前 UCI 兜底写死 `device=gnb_tun`。在单 zone 机器上恰好能用，在现网网关 182
// 这类机器上 `gnb_tun` 是**另一个 zone 正在用的接口** —— uci 照样 set 成功、
// fw4 照样按名字匹配，于是整台机器的转发口径挂到别人的隧道上且不报错。
// 接口名是阻断性配置，安装期没有权威时**宁可不绑**：首次 start
// （firewall.mynet start --interface）与 LuCI 的同步都会按实际名字补上。

func writeBundleJSON(t *testing.T, body string) string {
	t.Helper()

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "var"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "var", "bundle.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return home
}

func TestInstallTunDeviceFromBundle(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{"权威", `{"schema_version":2,"node":{"id":"1","interface":"gnb_tun_16"}}`, "gnb_tun_16", false},
		{"过渡别名", `{"schema_version":2,"node":{"id":"1"},"gnb":{"options":{"ifname":"gnb_tun_16"}}}`, "gnb_tun_16", false},
		{"两处不一致", `{"schema_version":2,"node":{"id":"1","interface":"gnb_tun_16"},"gnb":{"options":{"ifname":"gnb_tun"}}}`, "", true},
		{"皆空", `{"schema_version":2,"node":{"id":"1"}}`, "", true},
		{"非法字符", `{"schema_version":2,"node":{"id":"1","interface":"gnb tun;1"}}`, "", true},
	}
	for _, c := range cases {
		home := writeBundleJSON(t, c.body)
		got, err := installTunDevice(home)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%s: 应当报错，却拿到 %q", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Fatalf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// 没有 bundle（首次安装）= 拿不到权威，不是错误：返回空名，调用方跳过绑定。
func TestInstallTunDeviceWithoutBundleIsNoName(t *testing.T) {
	got, err := installTunDevice(t.TempDir())
	if err != nil {
		t.Fatalf("首次安装没有 bundle 不该报错: %v", err)
	}
	if got != "" {
		t.Fatalf("没有 bundle 却拿到接口名 %q", got)
	}
}

func TestOpenWrtUciStepsBindDeviceOnlyFromAuthority(t *testing.T) {
	join := func(steps [][]string) string {
		var b strings.Builder
		for _, s := range steps {
			b.WriteString(strings.Join(s, " "))
			b.WriteByte('\n')
		}

		return b.String()
	}

	// ① 没有权威 → 一条 device 写入都不许有
	noName := join(openWrtUciSteps(""))
	if strings.Contains(noName, "device=") {
		t.Fatalf("无 bundle 时不该写 device 绑定:\n%s", noName)
	}
	// 结构守恒：zone / forwarding / commit 仍然齐（别把修复做成少建 zone）
	for _, want := range []string{"network.mynet=interface", "name=mynet", "masq=1", "src=lan", "dest=lan", "commit firewall"} {
		if !strings.Contains(noName, want) {
			t.Fatalf("步骤缺了 %q:\n%s", want, noName)
		}
	}

	// ② 有权威 → network 与 zone 两处都绑同一个名字
	dev := join(openWrtUciSteps("gnb_tun_16"))
	if n := strings.Count(dev, "network.mynet.device=gnb_tun_16"); n != 1 {
		t.Fatalf("network.mynet.device 应出现 1 次，实际 %d:\n%s", n, dev)
	}
	if n := strings.Count(dev, "firewall.$s.device=gnb_tun_16"); n != 1 {
		t.Fatalf("zone 的 device 应出现 1 次，实际 %d:\n%s", n, dev)
	}
	// ③ 兜底路径里不许再出现任何写死的默认接口名（gnb_tun 不带 _16 的那种）
	if strings.Contains(dev, "device=gnb_tun ") || strings.Contains(dev, "device=gnb_tun\n") ||
		strings.Contains(dev, "device=wg0") {
		t.Fatalf("仍有写死的默认接口名:\n%s", dev)
	}
}

// 源码级红线：安装器整份文件不得内嵌驱动接口名字面量。
func TestOpenWrtInstallerHasNoHardcodedIfaceName(t *testing.T) {
	src, err := os.ReadFile("cmd_install_openwrt.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"gnb_tun", "wg0"} {
		if strings.Contains(string(src), bad) {
			t.Fatalf("cmd_install_openwrt.go 不该内嵌接口名 %q —— 只能来自 bundle 的 node.interface", bad)
		}
	}
}
