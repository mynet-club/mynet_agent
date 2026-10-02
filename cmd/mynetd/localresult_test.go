package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/mynetd/commands"
)

func TestLocalResultReporter_ReportsSuccess(t *testing.T) {
	var got commands.Result
	report := func(_ context.Context, res commands.Result) error {
		got = res
		return nil
	}
	fn := localResultReporter(report)
	fn("c1", true, "config refreshed", "lua")
	if got.ID != "c1" || !got.OK {
		t.Fatalf("got %+v", got)
	}
	if got.Stdout != "config refreshed" {
		t.Fatalf("stdout=%q", got.Stdout)
	}
	if got.Stderr != "executor=lua" {
		t.Fatalf("stderr=%q", got.Stderr)
	}
}

func TestLocalResultReporter_ReportsFailure(t *testing.T) {
	var got commands.Result
	report := func(_ context.Context, res commands.Result) error {
		got = res
		return nil
	}
	fn := localResultReporter(report)
	fn("c2", false, "mynet binary not found", "lua")
	if got.OK {
		t.Fatal("expected failure")
	}
	if got.Error != "mynet binary not found" {
		t.Fatalf("error=%q", got.Error)
	}
}

func TestLocalResultReporter_NilReportDoesNotPanic(t *testing.T) {
	fn := localResultReporter(nil)
	fn("c3", true, "ok", "lua") // 不应 panic
}

func TestLocalResultReporter_PropagatesReportError(t *testing.T) {
	// 上报失败不得 panic；由 logger 记录。这里只验证回调仍被调用且不吞。
	called := false
	report := func(_ context.Context, _ commands.Result) error {
		called = true
		return errors.New("transport down")
	}
	fn := localResultReporter(report)
	fn("c4", false, "x", "mynet")
	if !called {
		t.Fatal("report should be called even if it errors")
	}
}

func TestPlanLuaScript_AllowsKnownSubcommands(t *testing.T) {
	for _, argv := range [][]string{
		{"apply", "--auto"},
		{"apply", "--clear"},
		{"refresh-routes", "--auto"},
		{"refresh-config", "--auto"},
		{"refresh-address", "--auto"},
		{"service", "start"},
		{"metrics", "--json"},
		{"detector", "--json"},
	} {
		script, err := planLuaScript(argv)
		if err != nil {
			t.Fatalf("argv=%v: %v", argv, err)
		}
		if !strings.Contains(script, "MYNET_ARGV") {
			t.Fatalf("argv=%v script must read MYNET_ARGV", argv)
		}
	}
}

func TestPlanLuaScript_RejectsUnknown(t *testing.T) {
	if _, err := planLuaScript([]string{"curl", "http://evil"}); err == nil {
		t.Fatal("curl must be rejected")
	}
	if _, err := planLuaScript(nil); err == nil {
		t.Fatal("empty argv must fail")
	}
}

func TestSelectExecutor_DefaultIsMynet(t *testing.T) {
	t.Setenv("MYNET_EXECUTOR", "")
	if executorKind() != "mynet" {
		t.Fatalf("kind=%s", executorKind())
	}
}

func TestSelectExecutor_LuaWhenEnvSet(t *testing.T) {
	t.Setenv("MYNET_EXECUTOR", "lua")
	if executorKind() != "lua" {
		t.Fatalf("kind=%s", executorKind())
	}
}
