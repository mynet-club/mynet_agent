package main

import "testing"

func TestAccelMenuSeparatesStoppedHookFromRestorePreference(t *testing.T) {
	for _, tc := range []struct{ out, title, action, dns string }{
		{"alive=true wanted=true state=dns_overridden active=false", "加速：已中断", "重新开启加速", "DNS：已被外部更改 · 自动接管已暂停"},
		{"alive=true wanted=true state=dns_unknown active=false", "加速：状态待确认", "关闭加速", "DNS：无法确认系统接管状态"},
		{"alive=false wanted=true state=waiting_tunnel pins=0", "加速：已暂停", "取消自动恢复加速", "DNS：已停止 · 等待隧道连接"},
		{"alive=true wanted=true state=running pins=42", "加速：运行中", "关闭加速", "DNS：钩子运行中 · 钉扎 42"},
		{"alive=false wanted=false state=disabled pins=0", "加速：未开启", "开启加速", "DNS：已停止 · 未接管"},
		{"alive=false wanted=true state=failed pins=0", "加速：启动异常", "关闭加速", "DNS：未运行，请重新开启加速或查看诊断"},
	} {
		title, action, dns := accelMenuText(tc.out)
		if title != tc.title || action != tc.action || dns != tc.dns {
			t.Fatalf("%q: got %q, %q, %q", tc.out, title, action, dns)
		}
	}
}

func TestInterruptedAccelActionExplicitlyRestartsInsteadOfDisablingPreference(t *testing.T) {
	for _, tc := range []struct{ out, want string }{
		{"alive=true wanted=true state=dns_overridden active=false", "start"},
		{"alive=false wanted=true state=dns_overridden active=false", "start"},
		{"alive=true wanted=true state=running active=true", "stop"},
		{"alive=false wanted=true state=waiting_tunnel active=false", "stop"},
	} {
		if got := accelToggleOp(tc.out); got != tc.want {
			t.Fatalf("%s: %s", tc.out, got)
		}
	}
}
