package main

import "testing"

// 转移表穷尽测试：每条「状态 × 事件」的期望要么是明确转移，要么是显式 no-op。
// 漏写转移会在「应转移却 no-op」里炸出来。

func TestFSM_StartStopHappyPath(t *testing.T) {
	f := NewFSM()
	if f.State() != StateStopped {
		t.Fatalf("初始状态 = %s", f.State())
	}

	st, act, ok := f.On(EvStart)
	if !ok || st != StateStarting || act != ActionDoStart {
		t.Fatalf("Start: st=%s act=%s ok=%v", st, act, ok)
	}
	if !f.Busy() {
		t.Fatal("Starting 应为过渡态")
	}
	if f.CanStart() {
		t.Fatal("Starting 时不该能再点启动")
	}

	st, act, ok = f.On(EvDaemonUp)
	if !ok || st != StateRunning || act != ActionNone {
		t.Fatalf("DaemonUp: st=%s act=%s ok=%v", st, act, ok)
	}
	if !f.CanStop() {
		t.Fatal("Running 应能点停止")
	}

	st, act, ok = f.On(EvStop)
	if !ok || st != StateStopping || act != ActionDoStop {
		t.Fatalf("Stop: st=%s act=%s ok=%v", st, act, ok)
	}

	st, _, ok = f.On(EvDaemonDown)
	if !ok || st != StateStopped {
		t.Fatalf("DaemonDown: st=%s ok=%v", st, ok)
	}
}

func TestFSM_ColdStartWithDaemonAlreadyUp(t *testing.T) {
	f := NewFSM()
	st, _, ok := f.On(EvDaemonUp)
	if !ok || st != StateRunning {
		t.Fatalf("冷启动 daemon 已在跑: st=%s ok=%v", st, ok)
	}
}

func TestFSM_StartTimeout(t *testing.T) {
	f := NewFSM()
	f.On(EvStart)
	st, act, ok := f.On(EvStartTimeout)
	if !ok || st != StateFailed || act != ActionReportError {
		t.Fatalf("StartTimeout: st=%s act=%s ok=%v", st, act, ok)
	}
	if f.LastError() == "" {
		t.Fatal("Failed 应带 lastErr")
	}

	// 可重试
	st, act, ok = f.On(EvStart)
	if !ok || st != StateStarting || act != ActionDoStart {
		t.Fatalf("Failed 后重试: st=%s act=%s ok=%v", st, act, ok)
	}
	if f.LastError() != "" {
		t.Fatal("重试应清 lastErr")
	}
}

func TestFSM_StopTimeout(t *testing.T) {
	f := NewFSM()
	f.On(EvStart)
	f.On(EvDaemonUp)
	f.On(EvStop)
	st, act, ok := f.On(EvStopTimeout)
	if !ok || st != StateFailed || act != ActionReportError {
		t.Fatalf("StopTimeout: st=%s act=%s ok=%v", st, act, ok)
	}
}

func TestFSM_DegradedAndRecover(t *testing.T) {
	f := NewFSM()
	f.On(EvStart)
	f.On(EvDaemonUp)
	st, _, _ := f.On(EvNodeDegraded)
	if st != StateDegraded {
		t.Fatalf("Degraded: %s", st)
	}
	if !f.CanStop() {
		t.Fatal("Degraded 应能停")
	}
	st, _, _ = f.On(EvNodeHealthy)
	if st != StateRunning {
		t.Fatalf("恢复: %s", st)
	}
}

func TestFSM_CrashToStopped(t *testing.T) {
	f := NewFSM()
	f.On(EvStart)
	f.On(EvDaemonUp)
	st, _, _ := f.On(EvDaemonDown)
	if st != StateStopped {
		t.Fatalf("崩溃: %s", st)
	}
}

func TestFSM_StopFromStarting(t *testing.T) {
	f := NewFSM()
	f.On(EvStart)
	st, act, _ := f.On(EvStop)
	if st != StateStopping || act != ActionDoStop {
		t.Fatalf("Starting 时停止: st=%s act=%s", st, act)
	}
}

func TestFSM_UndefinedIsNoOp(t *testing.T) {
	f := NewFSM()
	// Stopped 时来 EvStartTimeout —— 无意义，必须 no-op
	st, act, ok := f.On(EvStartTimeout)
	if ok || st != StateStopped || act != ActionNone {
		t.Fatalf("未定义转移应 no-op: st=%s act=%s ok=%v", st, act, ok)
	}
	// Running 时来 EvStartTimeout —— 同样无意义
	f.On(EvDaemonUp)
	st, act, ok = f.On(EvStartTimeout)
	if ok || st != StateRunning || act != ActionNone {
		t.Fatalf("Running+StartTimeout 应 no-op: st=%s act=%s ok=%v", st, act, ok)
	}
}

// TestFSM_TransitionTable 穷尽核对核心边（防止回归时悄悄丢转移）。
func TestFSM_TransitionTable(t *testing.T) {
	cases := []struct {
		from State
		ev   Event
		want State
		act  Action
	}{
		{StateStopped, EvStart, StateStarting, ActionDoStart},
		{StateStopped, EvDaemonUp, StateRunning, ActionNone},
		{StateStopped, EvNodeHealthy, StateRunning, ActionNone},
		{StateStopped, EvNodeDegraded, StateDegraded, ActionNone},
		{StateStopped, EvNodeFailed, StateFailed, ActionNone},
		{StateStarting, EvDaemonUp, StateRunning, ActionNone},
		{StateStarting, EvStartTimeout, StateFailed, ActionReportError},
		{StateStarting, EvStop, StateStopping, ActionDoStop},
		{StateRunning, EvStop, StateStopping, ActionDoStop},
		{StateRunning, EvDaemonDown, StateStopped, ActionNone},
		{StateRunning, EvNodeDegraded, StateDegraded, ActionNone},
		{StateRunning, EvNodeFailed, StateFailed, ActionNone},
		{StateDegraded, EvNodeHealthy, StateRunning, ActionNone},
		{StateDegraded, EvStop, StateStopping, ActionDoStop},
		{StateDegraded, EvDaemonDown, StateStopped, ActionNone},
		{StateFailed, EvStart, StateStarting, ActionDoStart},
		{StateFailed, EvStop, StateStopping, ActionDoStop},
		{StateFailed, EvDaemonDown, StateStopped, ActionNone},
		{StateStopping, EvDaemonDown, StateStopped, ActionNone},
		{StateStopping, EvStopTimeout, StateFailed, ActionReportError},
	}
	for _, c := range cases {
		f := &FSM{state: c.from}
		st, act, ok := f.On(c.ev)
		if !ok || st != c.want || act != c.act {
			t.Errorf("%s + %s = %s/%s (ok=%v), want %s/%s",
				c.from, c.ev, st, act, ok, c.want, c.act)
		}
	}
}

func TestStateLabels(t *testing.T) {
	for _, s := range []State{StateStopped, StateStarting, StateRunning, StateDegraded, StateFailed, StateStopping} {
		if s.String() == "Unknown" {
			t.Errorf("%d 缺 String()", int(s))
		}
		if s.Label() == "未知" {
			t.Errorf("%s 缺 Label()", s)
		}
	}
}
