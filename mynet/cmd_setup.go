package mynet

// 旧 setup 的密钥生成 / gnbKeyType 词表 / confDir 路径助手。
// 落盘面已迁到 setup_v2.go（conf/local · conf/online），不再写 mynet.conf。

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// gnbKeyType 后端的 putKeys 白名单是 gnbkey,ed25519,wireguard,x25519,curve25519
// （NodeController.php:974）。gnb 节点要填 **gnbkey** 而不是 ed25519 ——
// 两套词表的翻译在后端侧 NodeKeyDomainService::algorithmForKeyType()（CPI-37）。
func gnbKeyType(vpnType string) string {
	if vpnType == "gnb" {
		return "gnbkey"
	}
	return "wireguard"
}

// confDir $MYNET_HOME/conf（兼容旧调用：credential / refresh）。
func confDir() string {
	return filepath.Join(utils.GetMyNetHome(), "conf")
}

// cmdStatus / cmdCheck / cmdNodes / cmdPlatform — R2-2 只读面
func cmdStatus(_ []string) int {
	snap, err := readSystemSnapshot()
	if err != nil {
		fmt.Printf("system.json: %v（先 mynet detector）\n", err)
	} else {
		fmt.Printf("os=%s kernel=%s ifaces=%d fp=%s\n",
			snap.OS, snap.Kernel, len(snap.Interfaces), short(snap.Fingerprint))
	}
	m := collectMetricsBrief()
	fmt.Printf("cpu=%.1f%% mem=%dMB load1=%.2f vpn=%d\n",
		m["cpu"], m["mem_used"], m["load1"], m["vpn"])
	// 缺口⑥：状态面带隧道健康 —— tun 在不在、挂没挂 VPN IP（服务 active≠隧道可用）
	home := utils.GetMyNetHome()
	fmt.Println("tun:", currentTunHealth(home).Summary())
	return 0
}

func cmdCheck(_ []string) int {
	fail := 0
	if _, err := readSystemSnapshot(); err != nil {
		fmt.Println("✗ system.json 缺失 — mynet detector")
		fail = 1
	} else {
		fmt.Println("✓ system.json")
	}
	// iproute2 仅 Linux 必需；桌面平台路由由 route/netstat 承担，
	// 缺失不算失败（此前 mac/win 上必报「✗ ip 不可用」——平台误判）。
	if goos() == "linux" {
		if !cmdExists("ip") {
			fmt.Println("✗ ip 不可用")
			fail = 1
		} else {
			fmt.Println("✓ ip")
		}
	} else {
		fmt.Println("· ip 跳过（" + goos() + " 平台路由不走 iproute2）")
	}
	if cmdExists("wg") {
		fmt.Println("✓ wg")
	} else {
		fmt.Println("· wg 缺失（wireguard 驱动不可用）")
	}
	if cmdExists("gnb_ctl") {
		fmt.Println("✓ gnb_ctl")
	} else {
		fmt.Println("· gnb_ctl 缺失（gnb 驱动不可用）")
	}
	return fail
}

func cmdNodes(rest []string) int {
	return nodeList()
}

func cmdPlatform(_ []string) int {
	fmt.Printf("%s/%s\n", goos(), goarch())
	d := platformDescriptor()
	fmt.Printf("platform:  %s\n", d.Platform)
	fmt.Printf("pkg:       %s\n", d.PkgManager)
	fmt.Printf("service:   %s\n", d.ServiceMgr)
	fmt.Printf("capab:     %s\n", strings.Join(d.Capabilities, ","))
	return 0
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
