package main

import (
	"strings"
	"testing"
)

func TestAccelMenuSeparatesStoppedHookFromRestorePreference(t *testing.T) {
	for _, tc := range []struct{ out, title, action, dns string }{
		{"alive=true wanted=true state=dns_overridden active=false", "加速：已中断", "重新开启加速", "DNS：已被外部更改 · 自动接管已暂停"},
		{"alive=true wanted=true state=dns_unknown active=false", "加速：状态待确认", "关闭加速", "DNS：无法确认系统接管状态"},
		{"alive=false wanted=true state=waiting_tunnel pins=0", "加速：已暂停", "启动加速", "DNS：已停止 · 等待隧道连接"},
		{"alive=true wanted=true state=running pins=42", "加速：运行中", "关闭加速", "DNS：钩子运行中 · 钉扎 42"},
		{"alive=false wanted=false state=disabled pins=0", "加速：未开启", "开启加速", "DNS：已停止 · 未接管"},
		{"alive=false wanted=true state=failed pins=0", "加速：启动异常", "启动加速", "DNS：未运行，请重试或查看诊断"},
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
		{"alive=false wanted=true state=waiting_tunnel active=false", "start"},
	} {
		if got := accelToggleOp(tc.out); got != tc.want {
			t.Fatalf("%s: %s", tc.out, got)
		}
	}
}

func TestAccelMenuTextStructuredPausedSaysStartNotStop(t *testing.T) {
	for _, tc := range []struct {
		wanted        bool
		state         string
		active        bool
		domains       int
		title, action string
	}{
		{true, "waiting_tunnel", false, 0, "加速：已暂停", "启动加速"},
		{true, "waiting_node", false, 0, "加速：已暂停", "启动加速"},
		{true, "waiting_dns", false, 0, "加速：已暂停", "启动加速"},
		{true, "failed", false, 0, "加速：启动异常", "启动加速"},
		{true, "running", true, 3, "加速：运行中", "关闭加速"},
		{false, "disabled", false, 0, "加速：未开启", "开启加速"},
		{true, "dns_overridden", false, 0, "加速：已中断", "重新开启加速"},
	} {
		title, action, _ := accelMenuTextStructured(tc.wanted, tc.state, tc.active, tc.domains)
		if title != tc.title || action != tc.action {
			t.Fatalf("%s wanted=%v active=%v: got %q, %q want %q, %q",
				tc.state, tc.wanted, tc.active, title, action, tc.title, tc.action)
		}
	}
}

func TestFormatExitProbeLine(t *testing.T) {
	if got := formatExitProbeLine(exitProbeBrief{}); got != "" {
		t.Fatalf("未监测应空串，得到 %q", got)
	}
	if got := formatExitProbeLine(exitProbeBrief{Monitoring: true, Target: "10.0.0.1", Reachable: true, RTTMs: 12.4}); got != "节点可达 12ms" {
		t.Fatalf("可达文案=%q", got)
	}
	if got := formatExitProbeLine(exitProbeBrief{Monitoring: true, NodeID: "1001", Reachable: true}); got != "节点可达" {
		t.Fatalf("无 rtt 文案=%q", got)
	}
	if got := formatExitProbeLine(exitProbeBrief{Monitoring: true, Target: "10.0.0.1", Reachable: false}); got != "节点不可达" {
		t.Fatalf("不可达文案=%q", got)
	}
	if got := formatExitProbeLine(exitProbeBrief{Monitoring: false}); got != "" {
		t.Fatalf("未监测且不可达应空串，得到 %q", got)
	}
	if got := formatExitProbeLine(exitProbeBrief{Monitoring: false, Reachable: true}); got != "节点未监测" {
		t.Fatalf("未监测文案=%q", got)
	}
}

func TestAccelNodeMenuText(t *testing.T) {
	cases := []struct {
		p    exitProbeBrief
		want string
	}{
		{exitProbeBrief{}, "未监测"},
		{exitProbeBrief{Monitoring: true, Reachable: true, RTTMs: 61.2, NodeID: "1001"}, "可达 61ms"},
		{exitProbeBrief{Monitoring: true, Reachable: true}, "可达"},
		{exitProbeBrief{Monitoring: true, Reachable: false, NodeID: "9"}, "不可达"},
		{exitProbeBrief{Monitoring: true, Reachable: false}, "不可达"},
	}
	for _, tc := range cases {
		got := accelNodeMenuText(tc.p)
		if got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
		if strings.Contains(got, "1001") || strings.Contains(got, "9") && tc.want == "不可达" {
			t.Fatalf("文案不得含 node_id: %q", got)
		}
	}
}
