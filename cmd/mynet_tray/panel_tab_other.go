//go:build !darwin

package main

// 非 macOS 继续交给系统默认处理器；不强制选择浏览器或开启远程调试。
func reusePanelTab(target, key string) bool { return false }
