//go:build !windows

package main

// 非 Windows 平台：无 Windows 服务宿主，走 systemd/launchd/procd。
func tryRunAsWindowsService() bool { return false }
