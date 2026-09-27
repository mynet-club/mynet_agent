//go:build !linux && !darwin && !windows

package detector

import "runtime"

// registerPlatformRouting 兜底：非 linux/darwin/windows 平台用 stub 路由探测。
func registerPlatformRouting(f *DetectorFactory) {
	f.RegisterRoutingDetector(runtime.GOOS, func(SystemDetector) RoutingDetector {
		return &StubRoutingDetector{}
	})
}
