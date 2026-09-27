//go:build !linux && !windows && !darwin

package detector

// registerPlatformSystem 兜底：非 linux/windows 平台用 stub 系统探测。
func registerPlatformSystem(f *DetectorFactory, platform string) {
	f.RegisterSystemDetector(platform, func() SystemDetector {
		return &StubSystemDetector{}
	})
}
