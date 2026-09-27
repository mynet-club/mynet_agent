//go:build darwin

package detector

import (
	"os"
	"os/exec"
	"strings"
)

// DarwinServiceDetector —— launchd（launchctl list / print）。
// 能力面与 LinuxServiceDetector 对齐：IsInstalled/IsEnabled/IsRunning/ServicePath。
type DarwinServiceDetector struct {
	systemDetector SystemDetector
}

func NewDarwinServiceDetector(sys SystemDetector) *DarwinServiceDetector {
	return &DarwinServiceDetector{systemDetector: sys}
}

func (d *DarwinServiceDetector) GetCacheKey(string) string { return "service:darwin" }
func (d *DarwinServiceDetector) IsSupported() bool         { return true }

func (d *DarwinServiceDetector) DetectService(ctx DetectionContext, serviceName string) (*ServiceInfo, error) {
	info := &ServiceInfo{
		Name:       serviceName,
		Type:       "launchd",
		Properties: map[string]string{},
	}
	// 候选 label：club.mynet.<name> / com.mynet.<name> / 原名
	labels := []string{
		"club.mynet." + serviceName,
		"com.mynet." + serviceName,
		serviceName,
	}
	if strings.Contains(serviceName, ".") {
		labels = append([]string{serviceName}, labels...)
	}

	// launchctl list | grep label
	if out, err := exec.Command("launchctl", "list").Output(); err == nil {
		s := string(out)
		for _, label := range labels {
			if strings.Contains(s, label) {
				info.IsInstalled = true
				info.Properties["label"] = label
				// 带 "-" 的行表示未运行；PID 行表示在跑
				for _, ln := range strings.Split(s, "\n") {
					if strings.Contains(ln, label) {
						fields := strings.Fields(ln)
						if len(fields) >= 1 && fields[0] != "-" {
							info.IsRunning = true
							info.Properties["pid"] = fields[0]
						} else {
							info.IsRunning = false
						}
						if len(fields) >= 3 {
							info.Properties["last_exit"] = fields[2]
						}
						break
					}
				}
				break
			}
		}
	}

	// plist 路径
	plistCandidates := []string{
		"/Library/LaunchDaemons/" + info.Properties["label"] + ".plist",
		"/Library/LaunchAgents/" + info.Properties["label"] + ".plist",
	}
	for _, p := range plistCandidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			info.IsInstalled = true
			info.ServicePath = p
			info.IsEnabled = true // LaunchDaemons 默认随加载启用
			break
		}
	}
	if info.IsInstalled && info.ServicePath != "" {
		// print 看 Disabled
		if label := info.Properties["label"]; label != "" {
			if out, err := exec.Command("launchctl", "print", "system/"+label).Output(); err == nil {
				s := string(out)
				if strings.Contains(s, "state = running") {
					info.IsRunning = true
				}
				if strings.Contains(s, "disabled = true") {
					info.IsEnabled = false
				}
				if i := strings.Index(s, "program = "); i >= 0 {
					rest := s[i+len("program = "):]
					if j := strings.Index(rest, "\n"); j > 0 {
						info.ExecutablePath = strings.TrimSpace(rest[:j])
					}
				}
			}
		}
	}
	if !info.IsInstalled {
		info.ErrorMsg = "launchd service not found"
	}
	return info, nil
}

func registerPlatformService(f *DetectorFactory, platform string) {
	f.RegisterServiceDetector(platform, func(sys SystemDetector) ServiceDetector {
		return NewDarwinServiceDetector(sys)
	})
}
