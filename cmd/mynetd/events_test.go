package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/mynetd/commands"
)

// recordingExec 记录被调用的 argv，可选阻塞以测单飞守卫。
type recordingExec struct {
	mu    sync.Mutex
	calls [][]string
	block chan struct{} // 非 nil 时每次执行都等它
}

func (r *recordingExec) run(argv []string) (string, string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, argv)
	r.mu.Unlock()
	if r.block != nil {
		<-r.block
	}
	return "ok", "", nil
}

func (r *recordingExec) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// waitCalls 轮询直到记录到 n 次调用，避免用 sleep 猜。
func (r *recordingExec) waitCalls(t *testing.T, n int) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := len(r.calls)
		r.mu.Unlock()
		if got >= n {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// logSink 收集日志。事件处理在独立 goroutine 上跑，故必须加锁——
// 否则 -race 会（正确地）报数据竞争。
type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) printf(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(f, a...))
}

func (l *logSink) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, " ")
}

// waitLog 轮询直到日志里出现 substr。
func (l *logSink) waitLog(t *testing.T, substr string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(l.joined(), substr) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func newTestDispatcher(exec func([]string) (string, string, error)) (*eventDispatcher, *logSink) {
	sink := &logSink{}
	d := &eventDispatcher{
		exec:   exec,
		nodeID: "node-9",
		logf:   sink.printf,
	}
	return d, sink
}

// 事件必须映射到既有的 commands.Plan argv，而不是各写一份命令表。
func TestEventRouteChangedRunsRefreshRoutes(t *testing.T) {
	ex := &recordingExec{}
	d, _ := newTestDispatcher(ex.run)

	d.Handle("route_changed", nil)

	if !ex.waitCalls(t, 1) {
		t.Fatal("route_changed 未触发执行")
	}
	got := ex.snapshot()[0]
	if !strings.Contains(got, "refresh-routes") || !strings.Contains(got, "--auto") {
		t.Errorf("argv = %q, 应含 refresh-routes --auto", got)
	}
	// node_id 必须带上，否则刷新的是错的节点
	if !strings.Contains(got, "node-9") {
		t.Errorf("argv = %q, 应含 node_id node-9", got)
	}
}

func TestEventBundleChangedRunsRefreshNodes(t *testing.T) {
	ex := &recordingExec{}
	d, _ := newTestDispatcher(ex.run)
	// bundle_changed 现在是**先拉后刷**（V1-10①）：refresh-config 读的
	// var/bundle.json 由 mynetd 从 back 拉下来，不先拉就只能读到旧配置或根本没有。
	// 未装配拉取链时的行为由 TestBundleChangedWithoutSyncerLogsWhatIsMissing 单独钉住。
	d.setBundleSyncer(func(context.Context) (string, error) { return "updated", nil })

	d.Handle("bundle_changed", map[string]string{"generation": "42"})

	if !ex.waitCalls(t, 1) {
		t.Fatal("bundle_changed 未触发执行")
	}
	if got := ex.snapshot()[0]; !strings.Contains(got, "refresh-config") {
		t.Errorf("argv = %q, 应含 refresh-config（执行器没有 refresh-nodes 这个域）", got)
	}
}

// command_queued 需要回 back 拉命令（V1-9 未实现）。
// 关键判据是**不得静默忽略**——必须留下明确日志，否则症状是
// 「back 下了命令但节点毫无反应」，比直接报错难查得多。
func TestEventCommandQueuedIsNotSilentlyIgnored(t *testing.T) {
	ex := &recordingExec{}
	d, logs := newTestDispatcher(ex.run)

	d.Handle("command_queued", map[string]string{"command_id": "cmd-1"})

	time.Sleep(100 * time.Millisecond)
	if len(ex.snapshot()) != 0 {
		t.Errorf("command_queued 不应触发本地执行（命令要回 back 拉），实际调了 %v", ex.snapshot())
	}
	if !logs.waitLog(t, "command_queued") {
		t.Errorf("必须记录 command_queued 未实现，实际日志: %q", logs.joined())
	}
	if !logs.waitLog(t, "cmd-1") {
		t.Errorf("日志应带上 command_id 便于排障，实际: %q", logs.joined())
	}
}

func TestEventUnknownIsLogged(t *testing.T) {
	ex := &recordingExec{}
	d, logs := newTestDispatcher(ex.run)

	d.Handle("something_new", nil)

	if !logs.waitLog(t, "something_new") {
		t.Errorf("未知事件应记日志，实际: %q", logs.joined())
	}
	time.Sleep(50 * time.Millisecond)
	if len(ex.snapshot()) != 0 {
		t.Error("未知事件不应触发执行")
	}
}

// 单飞守卫：同一时刻只允许一个刷新在跑。
// 多个 `mynet apply`/`refresh-*` 并发写 OS 路由与驱动配置是本项目踩过的坑。
//
// ⚠️ 「合并」不等于「丢弃」：在跑期间到达的事件按名字合并成一份，
// 守卫释放后**补跑一次**。丢弃的代价是「错过的那次推送永远不会再来了」
// （网关不补发），实测形态就是「back 改了配置，节点毫无反应」。
func TestEventDispatcherMergesConcurrentEvents(t *testing.T) {
	block := make(chan struct{})
	ex := &recordingExec{block: block}
	d, logs := newTestDispatcher(ex.run)
	d.setBundleSyncer(func(context.Context) (string, error) { return "updated", nil })

	d.Handle("route_changed", nil)
	// 等第一个真的进入执行（inFlight 已置位）
	if !ex.waitCalls(t, 1) {
		t.Fatal("第一个事件未开始执行")
	}

	// 第一个还在跑时再来事件 → 排队补跑，但绝不能并发执行
	d.Handle("route_changed", nil) // 同名，合并成一份
	d.Handle("bundle_changed", nil)
	time.Sleep(150 * time.Millisecond)

	if got := len(ex.snapshot()); got != 1 {
		t.Errorf("在跑期间不得并发执行，实际执行了 %d 次", got)
	}
	if !logs.waitLog(t, "合并补跑") {
		t.Errorf("合并时应记日志说明，实际: %q", logs.joined())
	}

	// 放行：排队的事件必须补跑（route 1 次 + bundle 生命周期 3 步 = 4）
	close(block)
	if !ex.waitCalls(t, 5) {
		t.Errorf("守卫释放后未补跑排队事件，实际调用 %d 次: %v",
			len(ex.snapshot()), ex.snapshot())
	}
	// 同名只补一次，不得按写入次数膨胀
	time.Sleep(200 * time.Millisecond)
	if got := len(ex.snapshot()); got != 5 {
		t.Errorf("同名事件应合并为一次补跑，实际调用 %d 次: %v", got, ex.snapshot())
	}
}

// ── 连接建立时的补拉（WS 下发自愈，替代「心跳兜底」那句假话）──────────────

func fixedSyncer(status string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return status, nil }
}

// 补拉到新版 → 必须走完整落地链（refresh-config → 重载驱动 → 装路由）。
// 只写盘不重载就是「收到了但没生效」那一类最难发现的故障。
func TestCatchUpBundleRunsLifecycleWhenUpdated(t *testing.T) {
	ex := &recordingExec{}
	d, logs := newTestDispatcher(ex.run)
	d.setBundleSyncer(fixedSyncer("updated"))

	d.catchUpBundle()

	if !ex.waitCalls(t, 3) {
		t.Fatalf("补拉到新版应跑三步落地链，实际: %v（日志 %q）", ex.snapshot(), logs.joined())
	}
	want := []string{"refresh-config", "reload", "apply"}
	for i, w := range want {
		if !strings.Contains(ex.snapshot()[i], w) {
			t.Errorf("第 %d 步 argv = %q, 应含 %q", i+1, ex.snapshot()[i], w)
		}
	}
}

// 304（unchanged）= 本地已是最新。此时**不得**重载驱动：
// 重连是常规事件（网络抖动就会重连），每次重连都 Stop+Start 等于周期性掐断隧道。
func TestCatchUpBundleSkipsWhenUnchanged(t *testing.T) {
	ex := &recordingExec{}
	d, _ := newTestDispatcher(ex.run)
	d.setBundleSyncer(fixedSyncer("unchanged"))

	d.catchUpBundle()
	time.Sleep(150 * time.Millisecond)

	if got := ex.snapshot(); len(got) != 0 {
		t.Errorf("bundle 未变更时不得重载驱动/装路由，实际: %v", got)
	}
}

// 拉取链未装配 / 拉取失败：只记日志，不执行，也不得谎报成功。
func TestCatchUpBundleLogsAndSkipsOnFailure(t *testing.T) {
	ex := &recordingExec{}
	d, logs := newTestDispatcher(ex.run)
	// 未装配
	d.catchUpBundle()
	if !logs.waitLog(t, "拉取链未装配") {
		t.Errorf("未装配时应说明缺什么，实际: %q", logs.joined())
	}

	d2, logs2 := newTestDispatcher(ex.run)
	d2.setBundleSyncer(func(context.Context) (string, error) {
		return "", errors.New("relay 超时")
	})
	d2.catchUpBundle()
	if !logs2.waitLog(t, "拉取失败") {
		t.Errorf("拉取失败应记日志，实际: %q", logs2.joined())
	}
	time.Sleep(100 * time.Millisecond)
	if got := ex.snapshot(); len(got) != 0 {
		t.Errorf("拉取失败不应执行任何落地步骤，实际: %v", got)
	}
}

// 执行失败必须记日志，不能静默。
func TestEventExecFailureIsLogged(t *testing.T) {
	ex := func(argv []string) (string, string, error) {
		return "", "route write failed", errors.New("exit status 1")
	}
	d, logs := newTestDispatcher(ex)

	d.Handle("route_changed", nil)

	if !logs.waitLog(t, "执行失败") {
		t.Errorf("执行失败应记日志，实际: %q", logs.joined())
	}
	if !logs.waitLog(t, "route write failed") {
		t.Errorf("失败日志应带 stderr 便于排障，实际: %q", logs.joined())
	}
}

// ── command_queued 的 pull 链路 ────────────────────────────────────

// scriptedPull 按脚本依次返回批次，用尽后返回空（模拟 back「每次只给 1 条」）。
type scriptedPull struct {
	mu      sync.Mutex
	batches [][]pendingCommand
	calls   int
	err     error
}

func (p *scriptedPull) next(_ context.Context) ([]pendingCommand, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	i := p.calls - 1
	if i >= len(p.batches) {
		return nil, nil
	}
	return p.batches[i], nil
}

func (p *scriptedPull) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type reportedResult struct {
	res commands.Result
}

func TestCommandQueuedPullsMapsExecutesReports(t *testing.T) {
	ex := &recordingExec{}
	pull := &scriptedPull{batches: [][]pendingCommand{
		{{ID: "9001", Command: "mynet.route.refresh", Params: map[string]any{"node_id": "77"}}},
	}}
	var reported []reportedResult
	var mu sync.Mutex
	report := func(_ context.Context, res commands.Result) error {
		mu.Lock()
		reported = append(reported, reportedResult{res})
		mu.Unlock()
		return nil
	}

	d, logs := newTestDispatcher(ex.run)
	d.pull = pull.next
	d.report = report

	d.Handle("command_queued", map[string]string{"command_id": "9001"})

	if !ex.waitCalls(t, 1) {
		t.Fatalf("command_queued 未触发执行；日志: %q", logs.joined())
	}
	// back 的 mynet.route.refresh 必须映射到 agent 的 route.refresh → refresh-routes --auto
	got := ex.snapshot()[0]
	if !strings.Contains(got, "refresh-routes") || !strings.Contains(got, "--auto") {
		t.Errorf("argv = %q, 应为 refresh-routes --auto", got)
	}
	// params 必须透传成 --node-id
	if !strings.Contains(got, "77") {
		t.Errorf("argv = %q, 应带上 params 里的 node_id 77", got)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(reported)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reported) == 0 {
		t.Fatal("结果未回报 —— back 侧那条命令会永远卡在「执行中」")
	}
	if reported[0].res.ID != "9001" {
		t.Errorf("回报的命令 ID = %q, want 9001", reported[0].res.ID)
	}
	if !reported[0].res.OK {
		t.Errorf("执行成功却回报 OK=false: %+v", reported[0].res)
	}
}

// back 下发 agent 不认识的命令时，**必须按失败回报**，不能静默丢弃。
// 这是 CPI-4 的核心教训：静默失效从 back 侧完全看不出来。
func TestCommandQueuedUnknownCommandReportsFailure(t *testing.T) {
	ex := &recordingExec{}
	pull := &scriptedPull{batches: [][]pendingCommand{
		{{ID: "9002", Command: "mynet.reboot"}}, // 历史上真实漂过的那一条
	}}
	var got []commands.Result
	var mu sync.Mutex
	d, logs := newTestDispatcher(ex.run)
	d.pull = pull.next
	d.report = func(_ context.Context, res commands.Result) error {
		mu.Lock()
		got = append(got, res)
		mu.Unlock()
		return nil
	}

	d.Handle("command_queued", map[string]string{"command_id": "9002"})

	if !logs.waitLog(t, "不认识的命令") {
		t.Fatalf("应明确记录不认识的命令，实际日志: %q", logs.joined())
	}
	time.Sleep(100 * time.Millisecond)
	if len(ex.snapshot()) != 0 {
		t.Errorf("不认识的命令不应被执行，实际调了 %v", ex.snapshot())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("应按失败回报 1 次，实际 %d 次", len(got))
	}
	if got[0].OK {
		t.Error("不认识的命令必须回报 OK=false")
	}
	if !strings.Contains(got[0].Error, "mynet.reboot") {
		t.Errorf("回报的错误应带上原始命令名便于定位，实际 %q", got[0].Error)
	}
}

// back 每次只返 1 条，所以要循环拉到空为止。
func TestCommandQueuedDrainsUntilEmpty(t *testing.T) {
	ex := &recordingExec{}
	pull := &scriptedPull{batches: [][]pendingCommand{
		{{ID: "1", Command: "mynet.metrics"}},
		{{ID: "2", Command: "mynet.detector"}},
		{{ID: "3", Command: "mynet.service.start"}},
	}}
	d, _ := newTestDispatcher(ex.run)
	d.pull = pull.next
	d.report = func(context.Context, commands.Result) error { return nil }

	d.Handle("command_queued", nil)

	if !ex.waitCalls(t, 3) {
		t.Fatalf("应连续拉取并执行 3 条，实际 %d 次: %v", len(ex.snapshot()), ex.snapshot())
	}
	if pull.callCount() < 4 {
		t.Errorf("应多拉一次以确认队列已空，实际拉了 %d 次", pull.callCount())
	}
}

// 拉取失败必须记日志，不能静默。
func TestCommandQueuedPullErrorIsLogged(t *testing.T) {
	ex := &recordingExec{}
	d, logs := newTestDispatcher(ex.run)
	d.pull = func(context.Context) ([]pendingCommand, error) {
		return nil, errors.New("gateway 不可达")
	}
	d.report = func(context.Context, commands.Result) error { return nil }

	d.Handle("command_queued", map[string]string{"command_id": "9"})

	if !logs.waitLog(t, "拉取待执行命令失败") {
		t.Errorf("拉取失败应记日志，实际: %q", logs.joined())
	}
	if !logs.waitLog(t, "gateway 不可达") {
		t.Errorf("日志应保留原始错误，实际: %q", logs.joined())
	}
}

// 回报失败必须吵出来：命令执行了但 back 不知道，会一直显示「执行中」。
func TestCommandQueuedReportErrorIsLogged(t *testing.T) {
	ex := &recordingExec{}
	pull := &scriptedPull{batches: [][]pendingCommand{{{ID: "5", Command: "mynet.metrics"}}}}
	d, logs := newTestDispatcher(ex.run)
	d.pull = pull.next
	d.report = func(context.Context, commands.Result) error {
		return errors.New("未连接到网关")
	}

	d.Handle("command_queued", nil)

	if !logs.waitLog(t, "结果回报失败") {
		t.Errorf("回报失败应记日志，实际: %q", logs.joined())
	}
}

// pull/report 未装配（无 gateway 或无节点身份）时必须明确说明缺什么。
func TestCommandQueuedWithoutPullChainIsLogged(t *testing.T) {
	ex := &recordingExec{}
	d, logs := newTestDispatcher(ex.run) // pull/report 均为 nil

	d.Handle("command_queued", map[string]string{"command_id": "cmd-x"})

	if !logs.waitLog(t, "命令拉取链未装配") {
		t.Errorf("应明确说明拉取链未装配，实际: %q", logs.joined())
	}
	if !logs.waitLog(t, "cmd-x") {
		t.Errorf("日志应带 command_id 便于排障，实际: %q", logs.joined())
	}
	time.Sleep(50 * time.Millisecond)
	if len(ex.snapshot()) != 0 {
		t.Error("未装配时不应执行任何命令")
	}
}

func TestStringifyParams(t *testing.T) {
	if got := stringifyParams(nil); got != nil {
		t.Errorf("nil params 应返回 nil，实际 %v", got)
	}
	if got := stringifyParams(map[string]any{}); got != nil {
		t.Errorf("空 params 应返回 nil，实际 %v", got)
	}

	got := stringifyParams(map[string]any{
		"node_id":  "77",
		"vpn_type": "gnb",
		"empty":    nil,
		"count":    float64(3),
		"flag":     true,
		"nested":   map[string]any{"a": "b"},
	})
	want := map[string]string{
		"node_id": "77", "vpn_type": "gnb", "empty": "",
		"count": "3", "flag": "true", "nested": `{"a":"b"}`,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("params[%q] = %q, want %q", k, got[k], w)
		}
	}
}
