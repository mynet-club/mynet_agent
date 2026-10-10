//go:build linux

package mynet

import "testing"

// isOpenWrt 在共享 linux 文件里（通用 Linux 后端也要用它做拒绝判定）。
// 开发/CI 机通常不是 OpenWrt，只要探测不 panic 即可。
func TestIsOpenWrtDoesNotPanic(t *testing.T) {
	_ = isOpenWrt()
}
