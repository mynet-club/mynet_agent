//go:build !linux

package detector

import (
	"fmt"
	"runtime"
)

// StubSystemDetector 存根系统检测器，用于非Linux平台
type StubSystemDetector struct{}

func (d *StubSystemDetector) DetectSystem(ctx DetectionContext) (*SystemInfo, error) {
	return &SystemInfo{
		OS:           runtime.GOOS,
		Architecture: runtime.GOARCH,
		Hostname:     "localhost",
		Uptime:       0,
		Kernel:       "unknown",
		Distribution: fmt.Sprintf("%s-stub", runtime.GOOS),
		Version:      "unknown",
		Properties:   make(map[string]string),
	}, nil
}

func (d *StubSystemDetector) GetCacheKey() string {
	return "stub-system"
}

func (d *StubSystemDetector) IsSupported() bool {
	return true
}

// StubRoutingDetector 存根路由检测器
type StubRoutingDetector struct{}

func (d *StubRoutingDetector) DetectRouting(ctx DetectionContext) (*RoutingInfo, error) {
	return &RoutingInfo{
		DefaultGateway: "unknown",
		DefaultIface:   "unknown",
		Interfaces:     []NetworkInterface{},
		Routes:         []RouteEntry{},
		Statistics: RouteStatistics{
			TotalInterfaces:  0,
			TotalRoutes:      0,
			NodeRoutes:       0,
			ActiveInterfaces: 0,
		},
	}, nil
}

func (d *StubRoutingDetector) GetCacheKey() string {
	return "stub-routing"
}

func (d *StubRoutingDetector) IsSupported() bool {
	return true
}

// StubFirewallDetector 存根防火墙检测器
type StubFirewallDetector struct{}

func (d *StubFirewallDetector) DetectFirewall(ctx DetectionContext) (*FirewallInfo, error) {
	return &FirewallInfo{
		Type:        fmt.Sprintf("%s-stub", runtime.GOOS),
		Enabled:     false,
		Rules:       []FirewallRule{},
		Zones:       []FirewallZone{},
		PortStates:  []PortState{},
		ChainStates: make(map[string]string),
		Properties:  make(map[string]interface{}),
	}, nil
}

func (d *StubFirewallDetector) GetCacheKey() string {
	return "stub-firewall"
}

func (d *StubFirewallDetector) IsSupported() bool {
	return true
}

// StubServiceDetector 存根服务检测器
type StubServiceDetector struct{}

func (d *StubServiceDetector) DetectService(ctx DetectionContext, serviceName string) (*ServiceInfo, error) {
	return &ServiceInfo{
		Name:              serviceName,
		Type:              fmt.Sprintf("%s-stub", runtime.GOOS),
		IsInstalled:       false,
		IsEnabled:         false,
		IsRunning:         false,
		ServicePath:       "",
		ConfigPath:        "",
		ExecutablePath:    "",
		WorkingDir:        "",
		ServiceUser:       "",
		ConfigConsistency: true,
		ErrorMsg:          "",
		Properties:        make(map[string]string),
	}, nil
}

func (d *StubServiceDetector) GetCacheKey(serviceName string) string {
	return fmt.Sprintf("stub-service-%s", serviceName)
}

func (d *StubServiceDetector) IsSupported() bool {
	return true
}

// registerPlatformSpecificDetectors 注册非Linux平台的检测器
func (f *DetectorFactory) registerPlatformSpecificDetectors() {
	platform := runtime.GOOS

	// 注册系统检测器
	f.RegisterSystemDetector(platform, func() SystemDetector {
		return &StubSystemDetector{}
	})

	// 注册路由检测器（darwin 有真实实现，见 routing_darwin.go）
	f.RegisterRoutingDetector(platform, func(systemDetector SystemDetector) RoutingDetector {
		if runtime.GOOS == "darwin" {
			return &DarwinRoutingDetector{}
		}
		return &StubRoutingDetector{}
	})

	// 注册防火墙检测器
	f.RegisterFirewallDetector(platform, func(systemDetector SystemDetector) FirewallDetector {
		return &StubFirewallDetector{}
	})

	// 注册服务检测器
	f.RegisterServiceDetector(platform, func(systemDetector SystemDetector) ServiceDetector {
		return &StubServiceDetector{}
	})
}
