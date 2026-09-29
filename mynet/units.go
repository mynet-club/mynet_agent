package mynet

import (
	_ "embed"
	"fmt"
)

// systemd 单元模板由 `mynet install` 写到 /etc/systemd/system/。
//
// ⚠️ 为什么放在 mynet/units/ 而不是 deploy/systemd/：`//go:embed` 的路径
// **不能越过包目录**（不能含 `..`），而 install 要在**分发出去的二进制**里
// 带着单元文件 —— 否则一台新机器上只拿到 mynet 二进制，`mynet install`
// 就只能像改前那样打印一句「请按平台执行 systemctl/launchctl」让人手抄，
// 那等于没装。单一来源：文件在这个包里，install 直接 embed，
// 不在 deploy/ 再放一份副本（副本会漂，见 CPI-56 的教训）。
//
// deploy/systemd/ 只保留**不归 mynet install 管**的单元
// （mynet_gateway.service 装在 back 服务器上），以及 env 示例。
//
//go:embed units/mynetd.service
var unitMynetd string

//go:embed units/mynet_gnb.service
var unitMynetGNB string

// OpenWrt procd init 脚本（E-1：procd 托管 gnb / mynetd）。
// 与 systemd 单元同一套 embed 原则：装机二进制自带，不依赖 deploy/ 副本。
//
//go:embed units/openwrt_mynetd.init
var initMynetd string

//go:embed units/openwrt_mynet.init
var initMynet string

// systemdUnit 按名字取单元正文。名字与 install/uninstall 的参数一致。
func systemdUnit(name string) (string, bool) {
	switch name {
	case "mynetd":
		return unitMynetd, true
	case "mynet_gnb", "gnb":
		return unitMynetGNB, true
	default:
		return "", false
	}
}

// unitFileName 落到 /etc/systemd/system/ 下的文件名。
func unitFileName(name string) string {
	if name == "gnb" {
		name = "mynet_gnb"
	}

	return name + ".service"
}

// SystemdUnitNames mynet install 会写的两个单元（顺序即启动顺序）。
var SystemdUnitNames = []string{"mynetd", "mynet_gnb"}

// OpenWrtInitNames procd init 脚本名（落到 /etc/init.d/）。
var OpenWrtInitNames = []string{"mynetd", "mynet"}

// openWrtInit 按名字取 procd init 正文。
func openWrtInit(name string) (string, bool) {
	switch name {
	case "mynetd":
		return initMynetd, true
	case "mynet":
		return initMynet, true
	default:
		return "", false
	}
}

// assertUnitsEmbedded 防呆：单元正文一旦被改得缺了关键指令，
// install 会装出一个**装得上但起不来**的服务，那种故障最难查。
func assertUnitsEmbedded() error {
	for _, n := range SystemdUnitNames {
		body, ok := systemdUnit(n)
		if !ok {
			return fmt.Errorf("单元 %s 未嵌入", n)
		}
		if !containsLine(body, "ExecStart=") {
			return fmt.Errorf("单元 %s 缺 ExecStart=", n)
		}
		if !containsLine(body, "[Service]") {
			return fmt.Errorf("单元 %s 缺 [Service] 段", n)
		}
	}
	for _, n := range OpenWrtInitNames {
		body, ok := openWrtInit(n)
		if !ok {
			return fmt.Errorf("procd init %s 未嵌入", n)
		}
		if !containsLine(body, "USE_PROCD=1") {
			return fmt.Errorf("procd init %s 缺 USE_PROCD=1", n)
		}
		if !containsLine(body, "start_service()") {
			return fmt.Errorf("procd init %s 缺 start_service()", n)
		}
	}
	return nil
}

func containsLine(body, prefix string) bool {
	for _, ln := range splitLines(body) {
		if len(ln) >= len(prefix) && ln[:len(prefix)] == prefix {
			return true
		}
	}

	return false
}
