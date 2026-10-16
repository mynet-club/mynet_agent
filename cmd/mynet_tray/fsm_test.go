package main

import "testing"

// 转移表测试。两条纪律：
//
//  1. 每条「状态 × 事件」要么明确转移，要么显式 no-op —— 漏写会炸出来；
//  2. 喂进来的事件必须是**产品真会产生**的（eventFromProbe / 菜单点击 /
//     超时判定）。旧版单测手工喂 EvDaemonUp 全绿，而 eventFromProbe 从不
//     产生它 ⇒ Starting 只能靠超时退出、Failed 成终态（D1/D2）。
//     探针驱动的那一半在 status_test.go 的 TestProbeFeedsFSM。

// fsmIn 造一个处于指定状态的 FSM（跳过冷启动路径）。
func fsmIn(s State) *FSM { return &FSM{state: s} }

func TestFSM_ColdStartIsSvcDown(t *testing.T) {
	f := NewFSM()
	// 还没探过就报「未连接」= 断言服务在跑而节点没连，是未经验证的结论。
	if f.State() != StateSvcDown {
		t.Fatalf("冷启动状态 = %s，want SvcDown", f.State())
	}
	if f.CanStart() {
		t.Fatal("SvcDown 时连接按钮不该可点（点了也打不到 API）")
	}
	if f.CanStop() {
		t.Fatal("SvcDown 时断开按钮不该可点")
	}
	if f.Busy() {
		t.Fatal("SvcDown 不是过渡态")
	}
	if f.LastError() == "" {
		// 冷启动没错误文案是对的：文案由 EvSvcDown 落。
		// 这里只确认 LastError 不 panic、不返回脏值。
		t.Logf("冷启动 lastErr 为空（由首个探针事件填充）")
	}
}

func TestFSM_StartStopHappyPath(t *testing.T) {
	f := NewFSM()

	// 探针报「服务在、节点没连」→ 脱离冷启动
	st, _, ok := f.On(EvDaemonDown)
	if !ok || st != StateStopped {
		t.Fatalf("SvcDown+DaemonDown: st=%s ok=%v", st, ok)
	}

	st, act, ok := f.On(EvStart)
	if !ok || st != StateStarting || act != ActionDoStart {
		t.Fatalf("Start: st=%s act=%s ok=%v", st, act, ok)
	}
	if !f.Busy() {
		t.Fatal("Starting 应为过渡态")
	}
	if f.CanStart() {
		t.Fatal("Starting 时不该能再点连接")
	}

	// D1：过渡态必须由**真实节点事实**结束
	st, act, ok = f.On(EvNodeHealthy)
	if !ok || st != StateRunning || act != ActionNone {
		t.Fatalf("NodeHealthy: st=%s act=%s ok=%v", st, act, ok)
	}
	if !f.CanStop() {
		t.Fatal("Running 应能点断开")
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

func TestFSM_ColdStartWithNodeAlreadyRunning(t *testing.T) {
	f := NewFSM()
	st, _, ok := f.On(EvNodeHealthy)
	if !ok || st != StateRunning {
		t.Fatalf("冷启动即在跑: st=%s ok=%v", st, ok)
	}
	if f.LastError() != "" {
		t.Fatalf("健康态不该留错误文案: %q", f.LastError())
	}
}

func TestFSM_StartingReachesAllThreeOutcomes(t *testing.T) {
	// D1 的正面断言：连接中三种真实结局都必须可达，不能只等超时。
	cases := []struct {
		ev   Event
		want State
	}{
		{EvNodeHealthy, StateRunning},
		{EvNodeDegraded, StateDegraded},
		{EvNodeFailed, StateFailed},
	}
	for _, c := range cases {
		f := fsmIn(StateStarting)
		st, _, ok := f.On(c.ev)
		if !ok || st != c.want {
			t.Errorf("Starting + %s = %s (ok=%v), want %s", c.ev, st, ok, c.want)
		}
	}
}

func TestFSM_StartTimeout(t *testing.T) {
	f := fsmIn(StateStopped)
	f.On(EvStart)
	st, act, ok := f.On(EvStartTimeout)
	if !ok || st != StateFailed || act != ActionReportError {
		t.Fatalf("StartTimeout: st=%s act=%s ok=%v", st, act, ok)
	}
	if f.LastError() != msgStartTimeout {
		t.Fatalf("超时应落超时文案，got %q", f.LastError())
	}

	// 可重试，且重试清错误
	st, act, ok = f.On(EvStart)
	if !ok || st != StateStarting || act != ActionDoStart {
		t.Fatalf("Failed 后重试: st=%s act=%s ok=%v", st, act, ok)
	}
	if f.LastError() != "" {
		t.Fatalf("重试应清 lastErr，got %q", f.LastError())
	}
}

// TestFSM_StartFailedKeepsReason 锁 §3「原始错误被超时文案覆盖」：
// 命令层已经拿到具体原因（如「当前节点未获准使用出口代理」），
// 落 EvStartFailed 时不得被通用的超时话术盖掉。
func TestFSM_StartFailedKeepsReason(t *testing.T) {
	f := fsmIn(StateStopped)
	f.On(EvStart)
	const reason = "当前节点未获准使用出口代理"
	f.lastErr = reason // 调用方（onUserStart）先写安全文案

	st, act, ok := f.On(EvStartFailed)
	if !ok || st != StateFailed || act != ActionReportError {
		t.Fatalf("StartFailed: st=%s act=%s ok=%v", st, act, ok)
	}
	if f.LastError() != reason {
		t.Fatalf("原因被覆盖: got %q, want %q", f.LastError(), reason)
	}

	// 已有原因时，随后到的超时也不许覆盖
	g := fsmIn(StateStarting)
	g.lastErr = reason
	g.On(EvStartTimeout)
	if g.LastError() != reason {
		t.Fatalf("超时覆盖了已知原因: got %q", g.LastError())
	}
}

func TestFSM_StopTimeout(t *testing.T) {
	f := fsmIn(StateRunning)
	f.On(EvStop)
	st, act, ok := f.On(EvStopTimeout)
	if !ok || st != StateFailed || act != ActionReportError {
		t.Fatalf("StopTimeout: st=%s act=%s ok=%v", st, act, ok)
	}
	if f.LastError() != msgStopTimeout {
		t.Fatalf("断开超时应落超时文案，got %q", f.LastError())
	}
}

func TestFSM_StopFailedKeepsReason(t *testing.T) {
	f := fsmIn(StateRunning)
	f.On(EvStop)
	const reason = "已有操作正在进行，请稍后再试"
	f.lastErr = reason
	st, act, ok := f.On(EvStopFailed)
	if !ok || st != StateFailed || act != ActionReportError {
		t.Fatalf("StopFailed: st=%s act=%s ok=%v", st, act, ok)
	}
	if f.LastError() != reason {
		t.Fatalf("原因被覆盖: got %q", f.LastError())
	}
}

// TestFSM_FailedIsNotTerminal 锁 D2：节点自己恢复了就该如实反映，
// 不该逼用户重启托盘。
func TestFSM_FailedIsNotTerminal(t *testing.T) {
	f := fsmIn(StateFailed)
	f.lastErr = msgNodeFailed
	st, _, ok := f.On(EvNodeHealthy)
	if !ok || st != StateRunning {
		t.Fatalf("Failed+NodeHealthy: st=%s ok=%v", st, ok)
	}
	if f.LastError() != "" {
		t.Fatalf("恢复后应清错误，got %q", f.LastError())
	}

	g := fsmIn(StateFailed)
	st, _, ok = g.On(EvNodeDegraded)
	if !ok || st != StateDegraded {
		t.Fatalf("Failed+NodeDegraded: st=%s ok=%v", st, ok)
	}
}

func TestFSM_DegradedAndRecover(t *testing.T) {
	f := fsmIn(StateRunning)
	st, _, _ := f.On(EvNodeDegraded)
	if st != StateDegraded {
		t.Fatalf("Degraded: %s", st)
	}
	if !f.CanStop() {
		t.Fatal("Degraded 应能断开")
	}
	st, _, _ = f.On(EvNodeHealthy)
	if st != StateRunning {
		t.Fatalf("恢复: %s", st)
	}
}

func TestFSM_CrashToStopped(t *testing.T) {
	f := fsmIn(StateRunning)
	st, _, _ := f.On(EvDaemonDown)
	if st != StateStopped {
		t.Fatalf("进程没了应判未连接而非异常: %s", st)
	}
	if f.LastError() != "" {
		t.Fatalf("未连接不是错误，got %q", f.LastError())
	}
}

func TestFSM_StopFromStarting(t *testing.T) {
	f := fsmIn(StateStopped)
	f.On(EvStart)
	st, act, _ := f.On(EvStop)
	if st != StateStopping || act != ActionDoStop {
		t.Fatalf("Starting 时断开: st=%s act=%s", st, act)
	}
}

// TestFSM_SvcDownBlocksEverything 锁 D3/D4：服务不可用时启停都是空转，
// 按钮必须禁用，文案必须是「服务不可用」而不是「未连接」。
func TestFSM_SvcDownBlocksEverything(t *testing.T) {
	for _, ev := range []Event{EvSvcDown, EvSvcInvalid} {
		f := fsmIn(StateRunning)
		st, act, ok := f.On(ev)
		if !ok || st != StateSvcDown || act != ActionNone {
			t.Fatalf("%s: st=%s act=%s ok=%v", ev, st, act, ok)
		}
		if f.CanStart() || f.CanStop() {
			t.Fatalf("%s 后按钮应全禁用", ev)
		}
		// 服务连不上时点连接：不转移、不发命令
		st, act, ok = f.On(EvStart)
		if ok || st != StateSvcDown || act != ActionNone {
			t.Fatalf("SvcDown+Start 应 no-op: st=%s act=%s ok=%v", st, act, ok)
		}
		st, act, ok = f.On(EvStop)
		if ok || st != StateSvcDown || act != ActionNone {
			t.Fatalf("SvcDown+Stop 应 no-op: st=%s act=%s ok=%v", st, act, ok)
		}
	}

	// 两种服务面失败的可读文案必须不同（P3 验收：服务离线/节点停止文案不同）
	d := fsmIn(StateStopped)
	d.On(EvSvcDown)
	i := fsmIn(StateStopped)
	i.On(EvSvcInvalid)
	if d.LastError() == i.LastError() {
		t.Fatalf("SvcDown 与 SvcInvalid 文案不该相同: %q", d.LastError())
	}
	if d.LastError() != msgSvcDown || i.LastError() != msgSvcInvalid {
		t.Fatalf("文案不符: down=%q invalid=%q", d.LastError(), i.LastError())
	}
}

func TestFSM_SvcRecoversWithoutRestart(t *testing.T) {
	cases := []struct {
		ev   Event
		want State
	}{
		{EvDaemonDown, StateStopped},
		{EvNodeHealthy, StateRunning},
		{EvNodeDegraded, StateDegraded},
		{EvNodeFailed, StateFailed},
	}
	for _, c := range cases {
		f := fsmIn(StateSvcDown)
		st, _, ok := f.On(c.ev)
		if !ok || st != c.want {
			t.Errorf("SvcDown + %s = %s (ok=%v), want %s", c.ev, st, ok, c.want)
		}
	}
}

func TestFSM_UndefinedIsNoOp(t *testing.T) {
	f := fsmIn(StateStopped)
	// Stopped 时来 EvStartTimeout —— 无意义，必须 no-op
	st, act, ok := f.On(EvStartTimeout)
	if ok || st != StateStopped || act != ActionNone {
		t.Fatalf("未定义转移应 no-op: st=%s act=%s ok=%v", st, act, ok)
	}
	// Running 时来 EvStartTimeout —— 同样无意义
	g := fsmIn(StateRunning)
	st, act, ok = g.On(EvStartTimeout)
	if ok || st != StateRunning || act != ActionNone {
		t.Fatalf("Running+StartTimeout 应 no-op: st=%s act=%s ok=%v", st, act, ok)
	}
	// Starting 时探针回声 DaemonDown 不该把过渡态打断（节点还没起来是正常的）
	h := fsmIn(StateStarting)
	st, act, ok = h.On(EvDaemonDown)
	if ok || st != StateStarting || act != ActionNone {
		t.Fatalf("Starting+DaemonDown 应 no-op: st=%s act=%s ok=%v", st, act, ok)
	}
	// Stopping 时节点又健康了：保持 Stopping，等下一轮事实
	k := fsmIn(StateStopping)
	st, act, ok = k.On(EvNodeHealthy)
	if ok || st != StateStopping || act != ActionNone {
		t.Fatalf("Stopping+NodeHealthy 应 no-op: st=%s act=%s ok=%v", st, act, ok)
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
		{StateStopped, EvNodeHealthy, StateRunning, ActionNone},
		{StateStopped, EvNodeDegraded, StateDegraded, ActionNone},
		{StateStopped, EvNodeFailed, StateFailed, ActionNone},
		{StateStopped, EvDaemonDown, StateStopped, ActionNone},

		{StateStarting, EvNodeHealthy, StateRunning, ActionNone},
		{StateStarting, EvNodeDegraded, StateDegraded, ActionNone},
		{StateStarting, EvNodeFailed, StateFailed, ActionNone},
		{StateStarting, EvStartFailed, StateFailed, ActionReportError},
		{StateStarting, EvStartTimeout, StateFailed, ActionReportError},
		{StateStarting, EvStop, StateStopping, ActionDoStop},

		{StateRunning, EvStop, StateStopping, ActionDoStop},
		{StateRunning, EvDaemonDown, StateStopped, ActionNone},
		{StateRunning, EvNodeDegraded, StateDegraded, ActionNone},
		{StateRunning, EvNodeFailed, StateFailed, ActionNone},
		{StateRunning, EvNodeHealthy, StateRunning, ActionNone},

		{StateDegraded, EvNodeHealthy, StateRunning, ActionNone},
		{StateDegraded, EvNodeFailed, StateFailed, ActionNone},
		{StateDegraded, EvStop, StateStopping, ActionDoStop},
		{StateDegraded, EvDaemonDown, StateStopped, ActionNone},

		{StateFailed, EvStart, StateStarting, ActionDoStart},
		{StateFailed, EvStop, StateStopping, ActionDoStop},
		{StateFailed, EvNodeHealthy, StateRunning, ActionNone},
		{StateFailed, EvNodeDegraded, StateDegraded, ActionNone},
		{StateFailed, EvDaemonDown, StateStopped, ActionNone},

		{StateStopping, EvDaemonDown, StateStopped, ActionNone},
		{StateStopping, EvStopFailed, StateFailed, ActionReportError},
		{StateStopping, EvStopTimeout, StateFailed, ActionReportError},
		{StateStopping, EvStop, StateStopping, ActionNone},

		{StateSvcDown, EvDaemonDown, StateStopped, ActionNone},
		{StateSvcDown, EvNodeHealthy, StateRunning, ActionNone},
		{StateSvcDown, EvNodeDegraded, StateDegraded, ActionNone},
		{StateSvcDown, EvNodeFailed, StateFailed, ActionNone},
		{StateSvcDown, EvStart, StateSvcDown, ActionNone},
		{StateSvcDown, EvStop, StateSvcDown, ActionNone},
	}
	for _, c := range cases {
		f := fsmIn(c.from)
		st, act, _ := f.On(c.ev)
		if st != c.want || act != c.act {
			t.Errorf("%s + %s = %s/%s, want %s/%s", c.from, c.ev, st, act, c.want, c.act)
		}
	}
}

// TestFSM_EveryEventHasAProducer 锁 D1/D2 的根因：转移表里用到的每种
// 「探针类」事件都必须真能从 eventFromProbe 产生，否则单测全绿而产品卡死。
func TestFSM_EveryEventHasAProducer(t *testing.T) {
	producible := map[Event]bool{}
	probes := []Probe{
		{Up: false},
		{Up: true, Valid: false},
		{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: true},
		{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: false},
		{Up: true, Valid: true, NodeState: "DEGRADED", ProcessUp: true},
		{Up: true, Valid: true, NodeState: "FAILED", ProcessUp: true},
		{Up: true, Valid: true, NodeState: "STOPPED"},
		{Up: true, Valid: true, NodeState: "UNINSTALLED"},
		{Up: true, Valid: true, NodeState: ""},
	}
	for _, p := range probes {
		producible[eventFromProbe(p)] = true
	}

	// 这四类不来自探针：两个是菜单点击，两个是本地命令失败。
	// 列出来是为了让「新增事件却没有产生者」在评审时显眼。
	nonProbe := map[Event]string{
		EvStart:       "菜单点击 onUserStart",
		EvStop:        "菜单点击 onUserStop",
		EvStartFailed: "startTunnel 返回错误 onUserStart",
		EvStopFailed:  "stopTunnel 返回错误 onUserStop",
		// 超时由 refreshOnce 的 deadline 判定产生
		EvStartTimeout: "refreshOnce deadline",
		EvStopTimeout:  "refreshOnce deadline",
	}

	for ev := Event(0); ev <= EvSvcInvalid; ev++ {
		if _, ok := nonProbe[ev]; ok {
			continue
		}
		if !producible[ev] {
			t.Errorf("%s 没有任何产生者（eventFromProbe 从不返回它）—— D1/D2 就是这么来的", ev)
		}
	}
}

func TestStateLabels(t *testing.T) {
	all := []State{StateStopped, StateStarting, StateRunning, StateDegraded, StateFailed, StateStopping, StateSvcDown}
	for _, s := range all {
		if s.String() == "Unknown" {
			t.Errorf("%d 缺 String()", int(s))
		}
		if s.Label() == "状态暂不可用" {
			t.Errorf("%s 缺 Label()", s)
		}
		if len([]rune(s.Label())) > MaxMenuRunes {
			t.Errorf("%s 的 Label 超过菜单上限 %d: %q", s, MaxMenuRunes, s.Label())
		}
	}
	// 状态短语互不相同，否则菜单上分不出来
	seen := map[string]State{}
	for _, s := range all {
		if prev, dup := seen[s.Label()]; dup {
			t.Errorf("Label 重复: %s 与 %s 都是 %q", prev, s, s.Label())
		}
		seen[s.Label()] = s
	}
}
