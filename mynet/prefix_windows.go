//go:build windows

package mynet

// defaultPrefix Windows 系统安装目录（INSTALL.md / PlatformDefaultHome 一致）。
// SCM 服务以 SYSTEM 跑，Program Files 是惯例且受 ACL 保护。
func defaultPrefix() string { return `C:\Program Files\MyNet` }
