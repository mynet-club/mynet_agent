//go:build windows

package utils

// ExeName Windows 可执行文件带 .exe。
func ExeName(base string) string { return base + ".exe" }
