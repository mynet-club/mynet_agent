//go:build linux

package detector

// registerPlatformSpecificDetectors 注册 Linux 平台的检测器
func (f *DetectorFactory) registerPlatformSpecificDetectors() {
	// 注册系统检测器
	f.RegisterSystemDetector("linux", func() SystemDetector {
		return NewLinuxSystemDetector()
	})

	// 注册路由检测器
	f.RegisterRoutingDetector("linux", func(systemDetector SystemDetector) RoutingDetector {
		return NewLinuxRoutingDetector(systemDetector)
	})

	// 注册防火墙检测器
	f.RegisterFirewallDetector("linux", func(systemDetector SystemDetector) FirewallDetector {
		return NewLinuxFirewallDetector(systemDetector)
	})

	// 注册服务检测器
	f.RegisterServiceDetector("linux", func(systemDetector SystemDetector) ServiceDetector {
		return NewLinuxServiceDetector(systemDetector)
	})
}
