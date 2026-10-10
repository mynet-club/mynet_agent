//go:build linux && openwrt

package mynet

import (
	_ "embed"
	"fmt"
)

// OpenWrt procd init 脚本（E-1：procd 托管 gnb / mynetd）。
// 与 systemd 单元同一套 embed 原则：装机二进制自带，不依赖 deploy/ 副本。
// 只进 `-tags openwrt` 产物 —— 桌面/通用 Linux 二进制不带它。

//go:embed units/openwrt_mynetd.init
var initMynetd string

//go:embed units/openwrt_mynet.init
var initMynet string

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

// assertOpenWrtInitEmbedded 防呆：init 缺 USE_PROCD/start_service 会装出一个起不来的服务。
func assertOpenWrtInitEmbedded() error {
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
