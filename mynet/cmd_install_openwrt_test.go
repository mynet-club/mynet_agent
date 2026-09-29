package mynet

import (
	"os"
	"strings"
	"testing"
)

func TestOpenWrtInitsEmbedded(t *testing.T) {
	if err := assertUnitsEmbedded(); err != nil {
		t.Fatalf("assertUnitsEmbedded: %v", err)
	}
	for _, name := range OpenWrtInitNames {
		body, ok := openWrtInit(name)
		if !ok {
			t.Fatalf("init %s missing", name)
		}
		if !strings.Contains(body, "USE_PROCD=1") {
			t.Fatalf("init %s must be procd (USE_PROCD=1)", name)
		}
		if !strings.Contains(body, "start_service()") {
			t.Fatalf("init %s missing start_service", name)
		}
		// 禁止 pkill/killall（安全红线）
		if strings.Contains(body, "pkill") || strings.Contains(body, "killall") {
			t.Fatalf("init %s must not pkill/killall", name)
		}
	}
}

func TestOpenWrtMynetdInitHasLuaExecutor(t *testing.T) {
	body, _ := openWrtInit("mynetd")
	if !strings.Contains(body, "MYNET_EXECUTOR=lua") {
		t.Fatal("mynetd init must set MYNET_EXECUTOR=lua")
	}
	if !strings.Contains(body, "8096") {
		t.Fatal("mynetd init must listen 8096")
	}
}

func TestOpenWrtMynetInitUsesProcdInstance(t *testing.T) {
	body, _ := openWrtInit("mynet")
	if !strings.Contains(body, "procd_open_instance") {
		t.Fatal("mynet init must use procd_open_instance for gnb")
	}
	if !strings.Contains(body, "respawn") {
		t.Fatal("mynet init must respawn gnb")
	}
	// stop 必须走 procd_kill + 精确清 gnb_es，而不是 Lua spawn/kill
	if !strings.Contains(body, "procd_kill") {
		t.Fatal("mynet init stop must procd_kill instance")
	}
	if !strings.Contains(body, "gnb_es") {
		t.Fatal("mynet init stop must clean gnb_es orphans")
	}
}

// 双驱动：init 必须按 VPN_TYPE 分支 gnb | wireguard（架构支持）
func TestOpenWrtMynetInitDualDriver(t *testing.T) {
	body, _ := openWrtInit("mynet")
	for _, want := range []string{
		"VPN_TYPE",
		"wireguard",
		"start_wg_interface",
		"stop_wg_interface",
		"wg_conf_path",
		"wg-quick",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("mynet init missing dual-driver piece %q", want)
		}
	}
	// gnb 路径不能写死成唯一分支
	if !strings.Contains(body, "start_gnb_instance") {
		t.Fatal("mynet init must keep gnb branch")
	}
}

func TestIsOpenWrtDoesNotPanic(t *testing.T) {
	// 开发机通常不是 OpenWrt，只要不 panic 即可
	_ = isOpenWrt()
}

// OpenWrt：mynet install 是**服务安装**，不 copy 二进制（文件落地归 ipk）。
// 全平台同一契约：安装包放文件，install 只装服务。
func TestOpenWrtInstallIsServiceOnly(t *testing.T) {
	body, _ := openWrtInit("mynetd")
	if !strings.Contains(body, "USE_PROCD=1") {
		t.Fatal("mynetd init must be procd")
	}
	for _, path := range []string{
		"cmd_install_openwrt.go",
		"cmd_install.go",
		"cmd_install_win.go",
	} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(src)
		// 禁「把自身/同目录二进制 copy 进前缀」的调用点
		// （func copyFile(src, dst 的定义本身不算）
		for _, bad := range []string{
			"copyFile(self,",
			"copyFile(src, filepath",
			"copyFile(gnbBin,",
		} {
			if strings.Contains(s, bad) {
				t.Fatalf("%s must NOT %s — package install places files", path, bad)
			}
		}
		if !strings.Contains(s, "安装包") && !strings.Contains(s, "ipk") {
			t.Fatalf("%s should mention package-install responsibility", path)
		}
	}
}

// OpenWrt 服务安装必须含 network/zone 配置（否则 enable 了也转发不通）
func TestOpenWrtInstallSetsNetworkZone(t *testing.T) {
	src, err := os.ReadFile("cmd_install_openwrt.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "setupOpenWrtNetworkZone") {
		t.Fatal("installOpenWrt must call setupOpenWrtNetworkZone")
	}
	if !strings.Contains(s, "network.mynet") || !strings.Contains(s, "firewall") {
		t.Fatal("setup must cover network.mynet + firewall zone")
	}
}
