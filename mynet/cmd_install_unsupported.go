//go:build !linux && !darwin && !windows

package mynet

import (
	"fmt"
	"os"
	"runtime"
)

// 未支持的 GOOS：明确 unsupported，禁止 error 桩后默认成功（playbook §4.1）。
// 目标平台只有 linux/openwrt/windows/darwin；其余平台构造即拒绝。

func platformInstall(_, _, _, _ string, _, _ bool) int {
	fmt.Fprintln(os.Stderr, "✗ 本二进制不含该平台的安装后端（仅支持 linux/openwrt/darwin/windows）")
	return 1
}

func platformUninstall(_ string, _, _ bool) int {
	fmt.Fprintln(os.Stderr, "✗ 本二进制不含该平台的卸载后端（仅支持 linux/openwrt/darwin/windows）")
	return 1
}

func platformDescriptor() PlatformDescriptor {
	if runtime.GOOS == "freebsd" || runtime.GOOS == "openbsd" || runtime.GOOS == "netbsd" {
		return PlatformDescriptor{Platform: runtime.GOOS, ServiceMgr: "manual", Capabilities: []string{"proxy-pc", "webui"}}
	}
	return PlatformDescriptor{Platform: "unsupported"}
}
