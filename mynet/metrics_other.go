//go:build !windows

package mynet

// diskUsedPercentOS 非 Windows 无专用实现，返回 -1 交回 df。
func diskUsedPercentOS(string) float64 { return -1 }
