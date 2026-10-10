//go:build windows

package mynet

// defaultPrefix Windows **程序安装目录**（放 mynet.exe/mynetd.exe）。
// 注意：这是 prefix 不是 home —— home 见 utils.PlatformDefaultHome（C:\ProgramData\MyNet）。
func defaultPrefix() string { return `C:\Program Files\MyNet` }
