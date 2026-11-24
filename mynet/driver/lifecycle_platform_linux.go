//go:build linux && !openwrt

package driver

import "os"

const (
	linuxProcdInitPath   = "/etc/init.d/mynet"
	linuxOpenWrtRelease  = "/etc/openwrt_release"
	linuxProcdBinaryPath = "/sbin/procd"
)

// platformServiceOwner 即使 Linux 二进制未带 -tags openwrt，只要运行在 OpenWrt/procd
// 环境中且 /etc/init.d/mynet 存在，也强制将 gnb/wg 生命周期委托给 procd，
// 防止 mynetd / mynet service 绕过 procd 额外 spawn 第二个 daemonized gnb 抢占 utc.lock。
func platformServiceOwner() string {
	return detectOpenWrtServiceOwner(linuxProcdInitPath, linuxOpenWrtRelease, linuxProcdBinaryPath)
}

func detectOpenWrtServiceOwner(initPath, releasePath, procdPath string) string {
	if st, err := os.Stat(initPath); err != nil || st.IsDir() {
		return ""
	}
	if _, err := os.Stat(releasePath); err == nil {
		return initPath
	}
	if _, err := os.Stat(procdPath); err == nil {
		return initPath
	}
	return ""
}
