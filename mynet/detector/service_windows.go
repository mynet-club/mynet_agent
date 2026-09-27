//go:build windows

package detector

import (
	"os/exec"
	"strings"
)

// WindowsServiceDetector —— sc query / Get-Service。
// 能力面与 LinuxServiceDetector 对齐。
type WindowsServiceDetector struct {
	systemDetector SystemDetector
}

func NewWindowsServiceDetector(sys SystemDetector) *WindowsServiceDetector {
	return &WindowsServiceDetector{systemDetector: sys}
}

func (d *WindowsServiceDetector) GetCacheKey(string) string { return "service:windows" }
func (d *WindowsServiceDetector) IsSupported() bool         { return true }

func (d *WindowsServiceDetector) DetectService(ctx DetectionContext, serviceName string) (*ServiceInfo, error) {
	info := &ServiceInfo{
		Name:       serviceName,
		Type:       "windows-service",
		Properties: map[string]string{},
	}
	// sc queryex 服务名
	out, err := exec.Command("sc", "queryex", serviceName).Output()
	s := string(out)
	if err != nil && strings.TrimSpace(s) == "" {
		// 试常见别名
		for _, alt := range []string{"mynetd", "MyNet", "mynet"} {
			if alt == serviceName {
				continue
			}
			if o, e := exec.Command("sc", "queryex", alt).Output(); e == nil {
				s = string(o)
				info.Properties["queried_as"] = alt
				break
			}
		}
	}
	if strings.Contains(strings.ToLower(s), "does not exist") || strings.TrimSpace(s) == "" {
		info.IsInstalled = false
		info.ErrorMsg = "service not found"
		return info, nil
	}
	info.IsInstalled = true
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "running"):
		info.IsRunning = true
	case strings.Contains(low, "stopped"):
		info.IsRunning = false
	}
	// STATE 行
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(strings.ToUpper(ln), "STATE") {
			info.Properties["state_line"] = strings.TrimSpace(ln)
		}
		if strings.Contains(strings.ToUpper(ln), "PID") {
			info.Properties["pid_line"] = strings.TrimSpace(ln)
		}
	}
	// 自启动：sc qc
	if q, err := exec.Command("sc", "qc", serviceName).Output(); err == nil {
		qs := strings.ToLower(string(q))
		if strings.Contains(qs, "auto_start") {
			info.IsEnabled = true
		} else if strings.Contains(qs, "demand_start") {
			info.IsEnabled = false
		}
		if i := strings.Index(qs, "binary_path_name"); i >= 0 {
			rest := string(q)[i:]
			if j := strings.Index(rest, "\n"); j > 0 {
				info.ExecutablePath = strings.TrimSpace(rest[len("BINARY_PATH_NAME"):j])
			}
		}
	}
	return info, nil
}

func registerPlatformService(f *DetectorFactory, platform string) {
	f.RegisterServiceDetector(platform, func(sys SystemDetector) ServiceDetector {
		return NewWindowsServiceDetector(sys)
	})
}
