//go:build linux && !openwrt

package mynet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Linux(systemd) 装配专属测试。

// TestSystemdUnitsAreEmbeddedAndWellFormed 钉住内嵌单元的最小完备性。
//
// 单元一旦被改得缺了 ExecStart / [Service]，`mynet install` 会装出一个
// **装得上但起不来**的服务 —— 而 install 还会报成功。那种故障只在启动那一刻
// 才暴露，最难查。所以这里在写盘之前就拦。
func TestSystemdUnitsAreEmbeddedAndWellFormed(t *testing.T) {
	if err := assertSystemdUnitsEmbedded(); err != nil {
		t.Fatalf("内嵌单元不完备: %v", err)
	}
	for _, name := range SystemdUnitNames {
		body, ok := systemdUnit(name)
		if !ok {
			t.Fatalf("单元 %s 未嵌入", name)
		}
		if !strings.Contains(body, "[Install]") {
			t.Errorf("单元 %s 缺 [Install] 段 —— systemctl enable 会失败", name)
		}
		if !strings.Contains(body, "WantedBy=multi-user.target") {
			t.Errorf("单元 %s 缺 WantedBy=multi-user.target", name)
		}
	}

	// gnb 那个必须自己管好自己的存活：数据面挂了没人拉起来，节点就「进程都在、网络没了」
	gnb, _ := systemdUnit("mynet_gnb")
	if !strings.Contains(gnb, "Restart=always") {
		t.Error("mynet_gnb 必须 Restart=always —— 隧道挂了要自动拉起（CPI-64 的教训）")
	}
	// mynetd 是编排者，崩了一样要起来，否则重启后没人装路由（CPI-65）
	md, _ := systemdUnit("mynetd")
	if !strings.Contains(md, "Restart=") {
		t.Error("mynetd 必须配置 Restart=（否则重启后无人重装内核路由，CPI-65）")
	}
}

func TestUnitFileNameMapping(t *testing.T) {
	if got := unitFileName("mynetd"); got != "mynetd.service" {
		t.Fatalf("unitFileName(mynetd) = %q", got)
	}
	// 别名 gnb 要落到 mynet_gnb.service，否则 install/uninstall 找不到对方写的文件
	if got := unitFileName("gnb"); got != "mynet_gnb.service" {
		t.Fatalf("unitFileName(gnb) = %q，应与 mynet_gnb 同名", got)
	}
	if _, ok := systemdUnit("gnb"); !ok {
		t.Fatal("systemdUnit(\"gnb\") 应命中 gnb 的单元（别名）")
	}
	if _, ok := systemdUnit("wireguard"); ok {
		t.Fatal("wireguard 尚无单元，不该命中")
	}
}

// TestWriteSystemdUnitsSubstitutesGnbPath 钉住 gnb 路径替换。
//
// systemd 的 ExecStart **第一个 token 必须是绝对路径**，不能是 ${VAR} ——
// 所以路径必须在安装时写进单元。不替换的话，gnb 不在 /usr/local/bin 时
// 单元装得上、起不来，而错误只在启动那一刻才出现。
func TestWriteSystemdUnitsSubstitutesGnbPath(t *testing.T) {
	dir := t.TempDir()
	custom := "/opt/gnb/bin/gnb"
	if err := writeSystemdUnits(dir, custom); err != nil {
		t.Fatal(err)
	}

	for _, name := range SystemdUnitNames {
		p := filepath.Join(dir, unitFileName(name))
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("未写出 %s: %v", p, err)
		}
		body := string(b)
		if name == "mynet_gnb" {
			if !strings.Contains(body, "ExecStart="+custom) {
				t.Errorf("mynet_gnb 的 ExecStart 未替换为 %s:\n%s", custom, body)
			}
			if strings.Contains(body, "/usr/local/bin/gnb") {
				t.Errorf("mynet_gnb 里还残留默认 gnb 路径:\n%s", body)
			}
		} else {
			if strings.Contains(body, custom) {
				t.Errorf("mynetd 单元不该被 gnb 路径替换影响:\n%s", body)
			}
		}
	}
}
