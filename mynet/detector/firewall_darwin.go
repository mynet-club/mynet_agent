//go:build darwin

package detector

import (
	"os/exec"
	"strings"
)

// DarwinFirewallDetector —— pfctl（macOS 自带）。
// 能力面与 LinuxFirewallDetector 对齐：Type / Enabled / Rules / PortStates。
type DarwinFirewallDetector struct {
	systemDetector SystemDetector
}

func NewDarwinFirewallDetector(sys SystemDetector) *DarwinFirewallDetector {
	return &DarwinFirewallDetector{systemDetector: sys}
}

func (d *DarwinFirewallDetector) GetCacheKey() string { return "firewall:darwin" }
func (d *DarwinFirewallDetector) IsSupported() bool   { return true }

func (d *DarwinFirewallDetector) DetectFirewall(ctx DetectionContext) (*FirewallInfo, error) {
	info := &FirewallInfo{
		Type:       "pf",
		Properties: map[string]interface{}{},
	}
	// pf 状态：Status: Enabled / Disabled
	if out, err := exec.Command("pfctl", "-s", "info").Output(); err == nil {
		s := string(out)
		info.Enabled = strings.Contains(s, "Status: Enabled") || strings.Contains(s, "Status: enabled")
		info.Properties["raw_status"] = firstLineContains(s, "Status:")
	}
	// 规则摘要（锚点行）
	if out, err := exec.Command("pfctl", "-s", "rules").Output(); err == nil {
		for _, ln := range strings.Split(string(out), "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			rule := FirewallRule{Comment: ln}
			// 粗解析：scrub/pass/block/drop + anchor
			low := strings.ToLower(ln)
			switch {
			case strings.Contains(low, "pass"):
				rule.Target = "pass"
			case strings.Contains(low, "block"):
				rule.Target = "block"
			case strings.Contains(low, "scrub"):
				rule.Target = "scrub"
			default:
				rule.Target = "other"
			}
			if i := strings.Index(low, "anchor"); i >= 0 {
				rule.Chain = "anchor"
			} else {
				rule.Chain = "main"
			}
			info.Rules = append(info.Rules, rule)
			if len(info.Rules) >= 50 {
				break
			}
		}
	}
	info.ChainStates = map[string]string{"main": "reported"}
	return info, nil
}

func firstLineContains(s, sub string) string {
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(ln, sub) {
			return strings.TrimSpace(ln)
		}
	}
	return ""
}

func registerPlatformFirewall(f *DetectorFactory, platform string) {
	f.RegisterFirewallDetector(platform, func(sys SystemDetector) FirewallDetector {
		return NewDarwinFirewallDetector(sys)
	})
}
