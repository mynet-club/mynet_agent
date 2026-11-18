//go:build !linux || !openwrt

package driver

// gnbExplicitPathArgs 非 OpenWrt 构建：gnb 默认就把 pid/ctl-block
// 放在 <ConfDir> 下（gnb_argv.c:855-864），无需显式指定。
func (d *GNBDriver) gnbExplicitPathArgs() []string { return nil }
