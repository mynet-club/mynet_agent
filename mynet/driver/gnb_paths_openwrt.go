//go:build linux && openwrt

package driver

import "path/filepath"

// gnbExplicitPathArgs 把 pid/ctl-block 显式钉回 ConfDir。
//
// OpenWrt 版 gnb 带 GNB_OPENWRT_BUILD，pid/ctl-block 默认落
// /tmp/gnb.<udp4_port>.{pid,map}（gnb_argv.c:845-854），而驱动判活
// （IsProcessAlive → <ConfDir>/gnb.pid）与 ctlStatus（<ConfDir>/gnb.map）
// 固定读 ConfDir；不钉路径时症状是「进程在跑但 status=false、gnb_ctl 打不开」。
//
// 走 argv 而非 node.conf：--systemd 下 node.conf 的 pid-file/ctl-block 被
// gnb_conf_file.c:336,349 忽略，argv 不受此限；direct spawn 与 procd 的
// `mynet utc driver-run` 共用本函数，三条启动路径口径一致。
func (d *GNBDriver) gnbExplicitPathArgs() []string {
	if d.ConfDir == "" {
		return nil
	}
	return []string{
		"--pid-file", filepath.Join(d.ConfDir, "gnb.pid"),
		"--ctl-block", filepath.Join(d.ConfDir, "gnb.map"),
	}
}
