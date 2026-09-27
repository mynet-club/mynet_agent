//go:build windows

package detector

import (
	"os/exec"
	"strings"
)

// WindowsFirewallDetector —— netsh advfirewall / sc sharedaccess。
// 能力面与 LinuxFirewallDetector 对齐。
type WindowsFirewallDetector struct {
	systemDetector SystemDetector
}

func NewWindowsFirewallDetector(sys SystemDetector) *WindowsFirewallDetector {
	return &WindowsFirewallDetector{systemDetector: sys}
}

func (d *WindowsFirewallDetector) GetCacheKey() string { return "firewall:windows" }
func (d *WindowsFirewallDetector) IsSupported() bool   { return true }

func (d *WindowsFirewallDetector) DetectFirewall(ctx DetectionContext) (*FirewallInfo, error) {
	info := &FirewallInfo{
		Type:        "windows-firewall",
		Properties:  map[string]interface{}{},
		ChainStates: map[string]string{},
	}
	// netsh advfirewall show allprofiles → 各 profile 状态
	if out, err := exec.Command("netsh", "advfirewall", "show", "allprofiles").Output(); err == nil {
		s := string(out)
		// State ON / OFF
		low := strings.ToLower(s)
		info.Enabled = strings.Contains(low, "state                                 on") ||
			strings.Contains(low, "state on") ||
			(strings.Contains(low, "on") && !strings.Contains(low, "state                                 off"))
		// 更稳：数 "State ON"
		onCount := strings.Count(s, "ON") + strings.Count(s, "On")
		offCount := strings.Count(s, "OFF") + strings.Count(s, "Off")
		info.Enabled = onCount > 0 && onCount >= offCount
		info.Properties["profiles_raw_on"] = onCount
		info.Properties["profiles_raw_off"] = offCount
	}
	// 当前 profile
	if out, err := exec.Command("netsh", "advfirewall", "show", "currentprofile").Output(); err == nil {
		info.Properties["current_profile"] = strings.TrimSpace(string(out))
	}
	// 规则摘要（前 30 条）
	if out, err := exec.Command("netsh", "advfirewall", "firewall", "show", "rule", "name=all").Output(); err == nil {
		for _, ln := range strings.Split(string(out), "\n") {
			ln = strings.TrimSpace(ln)
			low := strings.ToLower(ln)
			if strings.HasPrefix(low, "rule name:") {
				name := strings.TrimSpace(ln[len("Rule Name:"):])
				info.Rules = append(info.Rules, FirewallRule{Chain: "in/out", Comment: name, Target: "rule"})
			}
			if len(info.Rules) >= 30 {
				break
			}
		}
		info.Properties["rule_lines_scanned"] = true
	}
	info.ChainStates["profiles"] = "reported"
	return info, nil
}

func registerPlatformFirewall(f *DetectorFactory, platform string) {
	f.RegisterFirewallDetector(platform, func(sys SystemDetector) FirewallDetector {
		return NewWindowsFirewallDetector(sys)
	})
}
