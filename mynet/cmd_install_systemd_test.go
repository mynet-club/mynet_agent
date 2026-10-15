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
	home := t.TempDir() // 无 mode.json：-c 走 gnbConfPath 回退（缺口⑦的告警场景）
	custom := "/opt/gnb/bin/gnb"
	if err := writeSystemdUnits(dir, custom, home); err != nil {
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
			// 缺口⑦：__GNB_CONF__ 按 gnbConfPath(home) 替换（无 mode.json → 回退 default）
			if !strings.Contains(body, "-c /var/run/mynet/default") {
				t.Errorf("mynet_gnb 的 -c 未按 gnbConfPath(home) 替换:\n%s", body)
			}
			if strings.Contains(body, "__GNB_CONF__") {
				t.Errorf("占位符 __GNB_CONF__ 残留:\n%s", body)
			}
		} else {
			if strings.Contains(body, custom) {
				t.Errorf("mynetd 单元不该被 gnb 路径替换影响:\n%s", body)
			}
		}
	}
}

// TestWriteSystemdUnitsWarnsWhenModeJsonMissing 缺口⑦的告警用例：
// install 先于 setup 时 home 尚无 mode.json，单元按 gnbConfPath 回退写出
// （188 实测：拿着 /var/run/mynet/default crash-loop 50 次）。
// 单元**照写**（install 的职责是装服务），但「-c 待归位」必须显式警告 ——
// 静默写出坏单元正是那次事故的成因。反面：身份已定（mode.json 在位）不许噪音。
func TestWriteSystemdUnitsWarnsWhenModeJsonMissing(t *testing.T) {
	home := t.TempDir() // 无 mode.json
	warn := captureStderrFn(t, func() {
		if err := writeSystemdUnits(t.TempDir(), "/opt/gnb/bin/gnb", home); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(warn, "mynet_gnb.service 的 -c 待 mynet setup 后归位") {
		t.Fatalf("home 无 mode.json 时必须显式警告待归位，stderr=%q", warn)
	}
	if !strings.Contains(warn, "/var/run/mynet/default") {
		t.Fatalf("警告要指出单元暂指的回退目标，stderr=%q", warn)
	}

	// 单元照写：警告不许把 install 的落盘职责顶掉
	dir := t.TempDir()
	captureStderrFn(t, func() {
		if err := writeSystemdUnits(dir, "/opt/gnb/bin/gnb", home); err != nil {
			t.Error(err)
		}
	})
	b, err := os.ReadFile(filepath.Join(dir, unitFileName("mynet_gnb")))
	if err != nil {
		t.Fatalf("警告归警告，单元必须照写: %v", err)
	}
	if !strings.Contains(string(b), "-c /var/run/mynet/default") {
		t.Fatalf("单元应按回退目标照写（待 setup 归位）:\n%s", b)
	}

	// 身份已定（conf/mode.json 在位）→ 一个字都不许警告
	home2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home2, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home2, "conf", "mode.json"),
		[]byte(`{"mode":"local","active":{"node_id":"3001"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	warn2 := captureStderrFn(t, func() {
		if err := writeSystemdUnits(t.TempDir(), "/opt/gnb/bin/gnb", home2); err != nil {
			t.Error(err)
		}
	})
	if warn2 != "" {
		t.Fatalf("身份已定不该告警，stderr=%q", warn2)
	}
}
