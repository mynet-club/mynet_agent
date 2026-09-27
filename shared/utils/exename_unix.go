//go:build !windows

package utils

// ExeName 可执行文件后缀。unix 无扩展名。
func ExeName(base string) string { return base }
