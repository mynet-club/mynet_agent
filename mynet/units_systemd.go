//go:build linux && !openwrt

package mynet

import (
	_ "embed"
	"fmt"
)

//go:embed units/mynetd.service
var unitMynetd string

//go:embed units/mynet_gnb.service
var unitMynetGNB string

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

// assertSystemdUnitsEmbedded 防呆：单元正文一旦被改得缺了关键指令，
// install 会装出一个**装得上但起不来**的服务，那种故障最难查。
func assertSystemdUnitsEmbedded() error {
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

	return nil
}
