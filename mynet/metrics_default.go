//go:build !linux && !darwin && !windows

package mynet

// collectPlatformSystemMetrics 未覆盖平台如实记 note。
func collectPlatformSystemMetrics(m *Metrics) {
	m.Notes = append(m.Notes, "system metrics 未覆盖 "+runtime.GOOS)
}
