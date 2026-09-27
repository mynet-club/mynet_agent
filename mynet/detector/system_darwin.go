//go:build darwin

package detector

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DarwinSystemDetector —— 与 LinuxSystemDetector 能力面对齐。
// sw_vers / uname / sysctl.boottime，不依赖 gopsutil。
type DarwinSystemDetector struct{}

func NewDarwinSystemDetector() *DarwinSystemDetector { return &DarwinSystemDetector{} }

func (d *DarwinSystemDetector) GetCacheKey() string { return "system:darwin" }
func (d *DarwinSystemDetector) IsSupported() bool   { return runtime.GOOS == "darwin" }

func (d *DarwinSystemDetector) DetectSystem(ctx DetectionContext) (*SystemInfo, error) {
	info := &SystemInfo{
		OS:           "darwin",
		Distribution: "macOS",
		Architecture: runtime.GOARCH,
		Properties:   map[string]string{},
	}
	if h, err := os.Hostname(); err == nil {
		info.Hostname = h
	}
	if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
		info.Version = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("sw_vers", "-buildVersion").Output(); err == nil {
		info.Properties["build"] = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("uname", "-r").Output(); err == nil {
		info.Kernel = strings.TrimSpace(string(out))
	}
	// sysctl -n kern.boottime → { sec = 1712345678, usec = 0 } ...
	if out, err := exec.Command("sysctl", "-n", "kern.boottime").Output(); err == nil {
		s := string(out)
		if i := strings.Index(s, "sec = "); i >= 0 {
			rest := s[i+len("sec = "):]
			num := ""
			for _, c := range rest {
				if c >= '0' && c <= '9' {
					num += string(c)
				} else {
					break
				}
			}
			if sec, err := strconv.ParseInt(num, 10, 64); err == nil && sec > 0 {
				info.Uptime = time.Since(time.Unix(sec, 0))
			}
		}
	}
	info.Properties["platform"] = "macos"
	return info, nil
}

func registerPlatformSystem(f *DetectorFactory, platform string) {
	f.RegisterSystemDetector(platform, func() SystemDetector {
		return NewDarwinSystemDetector()
	})
}
