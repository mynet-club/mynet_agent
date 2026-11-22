//go:build !darwin

package mynet

// activeGnbUtunIface 非 macOS 平台无需通过内核控制 socket 反查 utun 归属。
func activeGnbUtunIface(home string) string { return "" }
