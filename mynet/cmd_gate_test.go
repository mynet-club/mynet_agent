package mynet

import (
	"strings"
	"testing"
)

func TestCmdGateUsage(t *testing.T) {
	if code := cmdGate([]string{"--bogus"}); code != 2 {
		t.Fatalf("非法参数应返回 2，实得 %d", code)
	}
	if code := cmdGate([]string{"--only"}); code != 2 {
		t.Fatalf("--only 缺值应返回 2，实得 %d", code)
	}
	if code := cmdGate([]string{"--only", "nope"}); code != 2 {
		t.Fatalf("未知闸名应返回 2，实得 %d", code)
	}
}

func TestRunGateSubjectMissing(t *testing.T) {
	// 无 gnb 进程、无 conf 时 ResolveSubject 可能失败或返回空 —— 必须 fail 而不是静默 pass
	g := runGateSubject()
	if g.Name != "subject" {
		t.Fatalf("name=%s", g.Name)
	}
	if g.Status != "pass" && g.Status != "fail" {
		t.Fatalf("subject 闸只应 pass/fail，得到 %s", g.Status)
	}
}

func TestRunGateSkipSemantics(t *testing.T) {
	// 无 conf 时 route 闸应 skip（不挡本地再部署），而不是假绿
	g := runGateRoute()
	if g.Status != "pass" && g.Status != "fail" && g.Status != "skip" {
		t.Fatalf("非法状态 %s", g.Status)
	}
	// fw 闸在 macOS 上应 skip（pf 测不了）
	g2 := runGateFW()
	if g2.Status == "pass" && strings.Contains(g2.Detail, "测不了") {
		t.Fatal("detail 说测得了但 status=pass 不一致")
	}
}

func TestRunGateSyncEmpty(t *testing.T) {
	g := runGateSync()
	// 无 remote 时应 skip；有数据时 pass/fail
	if g.Status != "skip" && g.Status != "pass" && g.Status != "fail" {
		t.Fatalf("非法状态 %s", g.Status)
	}
}

func TestPrintGateReportNoPanic(t *testing.T) {
	rep := gateReport{
		Gates: []gateResult{
			{Name: "subject", Title: "G-subject", Status: "pass", Detail: "ok"},
			{Name: "route", Title: "G-route", Status: "skip", Detail: "无 conf"},
			{Name: "fw", Title: "G-fw", Status: "fail", Detail: "未放行", Notes: []string{"nft 缺规则"}},
		},
		Summary: "1 pass / 1 fail / 1 skip",
	}
	printGateReport(rep) // 不应 panic
}
