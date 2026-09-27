//go:build !linux && !darwin && !windows

package detector

// registerPlatformFirewall 兜底：其余平台 stub 防火墙探测。
func registerPlatformFirewall(f *DetectorFactory, platform string) {
	f.RegisterFirewallDetector(platform, func(SystemDetector) FirewallDetector {
		return &StubFirewallDetector{}
	})
}

// registerPlatformService 兜底：其余平台 stub 服务探测。
func registerPlatformService(f *DetectorFactory, platform string) {
	f.RegisterServiceDetector(platform, func(SystemDetector) ServiceDetector {
		return &StubServiceDetector{}
	})
}
