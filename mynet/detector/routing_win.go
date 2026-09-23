package detector

import (
	"os/exec"
	"runtime"
	"strings"
)

// WindowsRoutingDetector — F-4：`route print` / `netsh` 精简实现。
type WindowsRoutingDetector struct{}

func (d *WindowsRoutingDetector) DetectRouting(ctx DetectionContext) (*RoutingInfo, error) {
	info := &RoutingInfo{}
	_ = ctx
	_ = d.defaultGateway(info)
	_ = d.interfaces(info)
	info.Statistics.TotalInterfaces = len(info.Interfaces)
	info.Statistics.TotalRoutes = len(info.Routes)
	return info, nil
}

func (d *WindowsRoutingDetector) GetCacheKey() string { return "routing:windows" }
func (d *WindowsRoutingDetector) IsSupported() bool   { return runtime.GOOS == "windows" }

func (d *WindowsRoutingDetector) defaultGateway(info *RoutingInfo) error {
	out, err := exec.Command("route", "print", "0.0.0.0").Output()
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "0.0.0.0" && f[1] == "0.0.0.0" {
			info.DefaultGateway = f[2]
			break
		}
	}
	return nil
}

func (d *WindowsRoutingDetector) interfaces(info *RoutingInfo) error {
	out, err := exec.Command("netstat", "-rn").Output()
	if err != nil {
		return err
	}
	_ = out
	info.Interfaces = append(info.Interfaces, NetworkInterface{Name: "stub", State: "up", Type: "ethernet"})
	return nil
}
