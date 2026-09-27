//go:build windows

package detector

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// WindowsSystemDetector — V5-2：真读，不再是 stub。
//
// 用 CIM（WMI）取 Caption/Version/LastBootUpTime，一次 PowerShell 调齐。
// 不用 `systeminfo`：它是全量文本、解析脆，而且中文系统表头又会本地化。
type WindowsSystemDetector struct{}

func NewWindowsSystemDetector() *WindowsSystemDetector { return &WindowsSystemDetector{} }

func (d *WindowsSystemDetector) DetectSystem(ctx DetectionContext) (*SystemInfo, error) {
	info := &SystemInfo{
		OS:           "windows",
		Architecture: runtime.GOARCH,
		Properties:   map[string]string{},
	}
	_ = ctx
	if hn, err := os.Hostname(); err == nil {
		info.Hostname = hn
	}
	d.detectVersion(info)
	return info, nil
}

func (d *WindowsSystemDetector) GetCacheKey() string { return "system:windows" }
func (d *WindowsSystemDetector) IsSupported() bool   { return runtime.GOOS == "windows" }

// registerPlatformSystem 供 factory_other.go（!linux）调用。
func registerPlatformSystem(f *DetectorFactory, platform string) {
	f.RegisterSystemDetector(platform, func() SystemDetector {
		return NewWindowsSystemDetector()
	})
}

func (d *WindowsSystemDetector) detectVersion(info *SystemInfo) {
	ps := `$os = Get-CimInstance Win32_OperatingSystem
@{
  caption = $os.Caption
  version = $os.Version
  build   = $os.BuildNumber
  boot    = $os.LastBootUpTime.ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
} | ConvertTo-Json -Compress`
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		// CIM 不可用时至少把架构带上，不留 "unknown"
		info.Distribution = "windows"
		info.Version = "unknown"
		info.Kernel = "unknown"
		return
	}
	var raw struct {
		Caption string `json:"caption"`
		Version string `json:"version"`
		Build   string `json:"build"`
		Boot    string `json:"boot"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		info.Distribution = "windows"
		info.Version = "unknown"
		info.Kernel = "unknown"
		return
	}
	// Caption 形如 "Microsoft Windows 10 Pro"，去掉 "Microsoft " 前缀
	info.Distribution = strings.TrimSpace(strings.TrimPrefix(raw.Caption, "Microsoft "))
	if info.Distribution == "" {
		info.Distribution = "windows"
	}
	info.Version = strings.TrimSpace(raw.Version)
	if raw.Build != "" {
		info.Properties["build"] = raw.Build
		if info.Version != "" {
			info.Version = info.Version + " (build " + raw.Build + ")"
		}
	}
	// Windows 的 "内核" 就是 OS 版本号
	info.Kernel = strings.TrimSpace(raw.Version)
	if t, err := time.Parse(time.RFC3339, raw.Boot); err == nil {
		info.Uptime = time.Since(t)
	}
}
