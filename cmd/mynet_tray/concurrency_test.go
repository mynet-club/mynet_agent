package main

// P2 并发缺陷复现（先复现再修，见 docs/TRAY_OPTIMIZATION_PLAYBOOK.md §P2）。
//
// 本文件**只新增**，不改任何产品代码：目标是让现有 App 的并发缺陷在
// `go test -race -count=1 -run 'TestConc' ./cmd/mynet_tray/` 下变红
// （断言失败或 race 报警）。修复（mu 保护 fsm/deadline/lastOneLine +
// 操作序号丢弃陈旧完成 + 新操作清理旧 deadline）落地后应全绿。
// 断言只走修复前后都存在 API：deadline 字段、fsm.State()/LastError()、
// stub 收到的请求数 —— 不引用任何新增字段/方法名。
//
// 三条纪律：
//   - 绝不触碰 systray.Run/AddMenuItem/SetTooltip/Quit（darwin 腿是无条件
//     cgo，没有事件循环会崩/挂）。菜单项用 &systray.MenuItem{ClickedCh: …}
//     手工构造（ClickedCh 是导出字段，零原生调用），clickLoop 只读这些
//     channel，因此可以驱动**真实** clickLoop。绝不向 mQuit 发信号
//     （clickLoop 收到它会调 systray.Quit）。
//   - saveIntent 落点：intent.go 的 intentDir() = os.UserConfigDir()/MyNet
//     （darwin=$HOME/Library/Application Support、windows=%AppData%、
//     linux=$XDG_CONFIG_HOME|$HOME/.config）。所以每个测试用 t.Setenv 把
//     HOME 与 AppData 双双指到 t.TempDir()，intent 文件关进临时目录，
//     绝不写真实用户配置目录。
//   - 测试收尾裸读 App 字段前必须 waitSettled：worker（修复前后都一样）
//     先写 fsm/deadline/lastErr、后调 render() → renderFn 原子自增。
//     等计数稳定即与 worker 的最后一次写建立 happens-before，-race 不会
//     把「测试读 vs 已结束 worker 写」误报成产品缺陷。
//
// 自带的测试替身（不复用 tray_test.go 的 apiStub：它的 reqs 切片无锁，
// 并发请求下替身自己就是 race 噪声源）：
//   - cycleProber：线程安全、脚本循环的 Prober；
//   - concStub：线程安全的 httptest 替身，可按路径计数、延迟响应、
//     用 gate 通道把 handler 扣住（模拟「启动 worker 还在飞」）。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/systray"
)

// ── 替身 ────────────────────────────────────────────────────────

// cycleProber 按脚本循环返回探针结果。mu 保证多 goroutine 并发 Probe 安全
// （fakeProber 的 i/calls 无锁，只适合单 goroutine）。
type cycleProber struct {
	mu     sync.Mutex
	script []Probe
	i      int
}

func (p *cycleProber) Probe() (Probe, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.script) == 0 {
		return Probe{Up: false}, nil
	}
	pr := p.script[p.i%len(p.script)]
	p.i++
	return pr, nil
}

// concStub 线程安全的本机 API 替身。
type concStub struct {
	srv *httptest.Server

	mu          sync.Mutex
	connectHits int
	commandHits int
	// 连接与断开同打 commandPath，靠 argv 区分；闸门只扣「启动」
	// （断开必须立刻返回，否则测不出「陈旧完成」这类时序）。
	startHits   int
	stopHits    int
	startGate   chan struct{} // 非 nil：启动请求阻塞等它关闭
	startDelay  time.Duration
	startCode   int
	startBody   string
	stopCode    int
	stopBody    string
	connectCode int
	connectBody string
}

func newConcStub(t *testing.T) *concStub {
	t.Helper()
	s := &concStub{
		connectCode: http.StatusOK,
		connectBody: `{"success":true}`,
		startCode:   http.StatusOK,
		startBody:   `{"success":true}`,
		stopCode:    http.StatusOK,
		stopBody:    `{"success":true}`,
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(func() {
		s.releaseStart() // 别把扣住的 handler 留到 Close 之后
		s.srv.Close()
	})
	return s
}

func (s *concStub) base() string { return strings.TrimPrefix(s.srv.URL, "http://") }

func (s *concStub) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var gate chan struct{}
	var delay time.Duration
	var code int
	var body string
	switch r.URL.Path {
	case connectPath:
		s.connectHits++
		code, body = s.connectCode, s.connectBody
	case commandPath:
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		// 加速状态轮询是读操作，不能计为一次启动，也不能被启动闸门扣住。
		if bytes.Contains(raw, []byte(`"dns-hook","status"`)) {
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"stdout":"alive=false wanted=false state=disabled"}}`))
			return
		}
		s.commandHits++
		// 启停同端点（见 tray.go:startTunnel）：按 argv 分开计数，
		// 闸门/延迟只作用于「启动」—— 断开被扣住就测不出并发时序了。
		if bytes.Contains(raw, []byte(`"stop"`)) {
			s.stopHits++
			code, body = s.stopCode, s.stopBody
		} else {
			s.startHits++
			gate, delay = s.startGate, s.startDelay
			code, body = s.startCode, s.startBody
		}
	default:
		s.mu.Unlock()
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		return
	}
	s.mu.Unlock()

	if gate != nil {
		<-gate
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

func (s *concStub) hits(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if path == connectPath {
		return s.connectHits
	}
	return s.commandHits
}

// gateStart 让后续 connect 请求扣在服务器里，直到 releaseStart。
func (s *concStub) gateStart() {
	s.mu.Lock()
	if s.startGate == nil {
		s.startGate = make(chan struct{})
	}
	s.mu.Unlock()
}

func (s *concStub) releaseStart() {
	s.mu.Lock()
	g := s.startGate
	s.startGate = nil
	s.mu.Unlock()
	if g != nil {
		close(g)
	}
}

func (s *concStub) setStartDelay(d time.Duration) {
	s.mu.Lock()
	s.startDelay = d
	s.mu.Unlock()
}

// failStart 让「启动」返回 200+success:false（业务失败，D5 同款信封）。
func (s *concStub) failStart(message string) {
	b, _ := json.Marshal(map[string]any{"success": false, "message": message})
	s.mu.Lock()
	s.startCode, s.startBody = http.StatusOK, string(b)
	s.mu.Unlock()
}

// failStop 让「断开」返回 200+success:false。
func (s *concStub) failStop(message string) {
	b, _ := json.Marshal(map[string]any{"success": false, "message": message})
	s.mu.Lock()
	s.stopCode, s.stopBody = http.StatusOK, string(b)
	s.mu.Unlock()
}

// starts / stops 按语义取计数：两者同打 commandPath，用总数会互相串味。
func (s *concStub) starts() int { s.mu.Lock(); defer s.mu.Unlock(); return s.startHits }
func (s *concStub) stops() int  { s.mu.Lock(); defer s.mu.Unlock(); return s.stopHits }

// waitHits 阻塞到 path 至少收到 n 个请求（3s 上限，防挂死）。
func (s *concStub) waitHits(t *testing.T, path string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for s.hits(path) < n {
		if time.Now().After(deadline) {
			t.Fatalf("等 %s 请求超时：got %d, want %d", path, s.hits(path), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// ── App 组装 ────────────────────────────────────────────────────

var concProbes = struct {
	stopped Probe
	running Probe
	failed  Probe
	down    Probe
}{
	stopped: Probe{Up: true, Valid: true, NodeState: "STOPPED", OneLine: "已停止"},
	running: Probe{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: true, OneLine: "运行中"},
	failed:  Probe{Up: true, Valid: true, NodeState: "FAILED", ProcessUp: true, OneLine: "节点失败"},
	down:    Probe{Up: false}, // → EvSvcDown
}

// newConcApp 造一个不碰 systray 原生的 App：
//   - renderFn 换成原子计数（并发 render 不在替身里造 race 噪声），计数
//     同时是 waitSettled 的收敛判据；
//   - 7 个菜单项手工构造（只用导出字段 ClickedCh，零 cgo），clickLoop 可用；
//   - HOME/AppData 指到 t.TempDir()，saveIntent 不写真实目录（见文件头）。
func newConcApp(t *testing.T, base string, pr Prober) (*App, *int64) {
	t.Helper()
	isolateUserConfig(t)
	a := NewApp(pr, base)
	a.hc.Timeout = 5 * time.Second
	renders := new(int64)
	a.renderFn = func() { atomic.AddInt64(renders, 1) }
	mk := func() *systray.MenuItem { return &systray.MenuItem{ClickedCh: make(chan struct{})} }
	a.mStatus = mk()
	a.mToggle = mk()
	a.mCopyIP = mk()
	a.mPanel = mk()
	a.mDiag = mk()
	a.mAuto = mk()
	a.mAccel = mk()
	a.mAccelOn = mk()
	a.mAccelDNS = mk()
	a.mAccelDom = mk()
	a.mQuit = mk() // 只构造、永不发信号：clickLoop 收到 mQuit 会调 systray.Quit（cgo）
	return a, renders
}

// waitSettled 等在飞的 worker 全部落完，并与它们的最后一次写建立
// happens-before（worker 写完状态必调 render → 原子计数；计数连续两个
// 窗口不变即认为收敛）。此后测试裸读 App 字段不会被 -race 误报。
func waitSettled(t *testing.T, renders *int64) {
	t.Helper()
	limit := time.Now().Add(3 * time.Second)
	prev := atomic.LoadInt64(renders)
	stable := 0
	for time.Now().Before(limit) {
		time.Sleep(30 * time.Millisecond)
		cur := atomic.LoadInt64(renders)
		if cur == prev {
			stable++
			if stable >= 2 {
				return
			}
		} else {
			stable = 0
		}
		prev = cur
	}
}

// pollLoop 起一个高频 refreshOnce 的 goroutine（模拟 3s 轮询的加速版），
// close(stop) 后退出；返回的等待函数收敛该 goroutine（建立 happens-before）。
func pollLoop(a *App, stop <-chan struct{}, interval time.Duration) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			a.refreshOnce()
			time.Sleep(interval)
		}
	}()
	return func() { <-done }
}

func validState(st State) bool {
	switch st {
	case StateStopped, StateStarting, StateRunning, StateDegraded,
		StateFailed, StateStopping, StateSvcDown:
		return true
	}
	return false
}

// derivedMsgs 从 FSM 的真实行为推导预期错误文案，不硬编码 msg* 常量：
// 修复若调整文案，断言跟着事实走。
func derivedMsgs() (svcDownMsg, nodeFailedMsg string) {
	f := NewFSM()
	f.On(EvSvcDown) // SvcDown，落服务不可用文案
	svcDownMsg = f.LastError()

	g := NewFSM()
	g.On(EvSvcDown)
	g.On(EvDaemonDown) // → Stopped，清错误
	g.On(EvNodeFailed) // → Failed，落节点失败文案
	nodeFailedMsg = g.LastError()
	return svcDownMsg, nodeFailedMsg
}

// ── 1. 轮询与点击并发 ───────────────────────────────────────────

// TestConc_PollAndClickRace 复现：refreshOnce（轮询）、onUserStart/Stop
// （点击）、worker goroutine（命令完成回写）、statusText（渲染取数）四方
// 并发读写 App.fsm（state/lastErr）、a.deadline、a.lastOneLine，全程无锁。
// -race 应报这三处的数据竞争。
func TestConc_PollAndClickRace(t *testing.T) {
	stub := newConcStub(t)
	a, renders := newConcApp(t, stub.base(),
		&cycleProber{script: []Probe{concProbes.stopped, concProbes.running}})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // 点击方：连接/断开交替（Can* 各自把关）
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				a.onUserStart()
			} else {
				a.onUserStop()
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	go func() { // 渲染方：持续取 (title, tooltip)
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			a.statusText()
			time.Sleep(time.Millisecond)
		}
	}()
	waitPoll := pollLoop(a, stop, time.Millisecond) // 轮询方

	time.Sleep(400 * time.Millisecond)
	close(stop)
	wg.Wait()
	waitPoll()
	waitSettled(t, renders)

	if st := a.fsm.State(); !validState(st) {
		t.Errorf("状态机落到非法状态: %v", st)
	}
	if stub.hits(commandPath)+stub.hits(commandPath) == 0 {
		t.Error("stub 一个请求都没收到：并发场景没有真实跑起来")
	}
}

// ── 2. 重复点击 ─────────────────────────────────────────────────

// TestConc_RepeatedClicks 复现：CanStart 判定与 EvStart 投递之间没有
// 串行化（check-then-act），并发点击「连接」会打出多于一次 connect；
// 以及连接中再点连接/点断开时请求数与状态的一致性。
func TestConc_RepeatedClicks(t *testing.T) {
	t.Run("并发爆点连接最多打一次connect", func(t *testing.T) {
		stub := newConcStub(t)
		a, renders := newConcApp(t, stub.base(), &cycleProber{script: []Probe{concProbes.stopped}})
		a.refreshOnce() // SvcDown → Stopped，CanStart=true
		if !a.fsm.CanStart() {
			t.Fatalf("前置失败：state=%s, CanStart=false", a.fsm.State())
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start // 同一起跑线，最大化 check-then-act 窗口
				a.onUserStart()
			}()
		}
		close(start)
		wg.Wait()
		waitSettled(t, renders)

		if n := stub.hits(commandPath); n > 1 {
			t.Errorf("并发点击打出 %d 次 %s (want ≤1)：Starting 期间 CanStart 应为 false，判定与投递没有串行化", n, commandPath)
		}
		if st := a.fsm.State(); !validState(st) {
			t.Errorf("状态机落到非法状态: %v", st)
		}
	})

	t.Run("进度期间重复点击不重复下发走真实clickLoop", func(t *testing.T) {
		stub := newConcStub(t)
		stub.gateStart() // 把启动 worker 扣在「还在飞」
		a, renders := newConcApp(t, stub.base(), &cycleProber{script: []Probe{concProbes.stopped}})
		a.refreshOnce() // → Stopped

		// 驱动真实 clickLoop（playbook P2：不能另写一套测试专用编排）。
		// goroutine 泄漏到测试结束即可：绝不向 mQuit 发信号（会走 systray.Quit → cgo）。
		go a.clickLoop()

		a.mToggle.ClickedCh <- struct{}{} // 点一下 → 连接
		stub.waitHits(t, commandPath, 1)

		// P4 合并后进度期间这一项是**禁用**的（toggleFor 的 Busy 分支），
		// 但 ClickedCh 仍可能被投递（用户狂点、合成事件）：每一次都必须 no-op，
		// 既不许再发一次 connect，也不许把这次点击解释成「断开」。
		// 「Starting 中投 EvStop 会转 Stopping」这条 FSM 能力仍在
		// （fsm_test.go TestFSM_StopFromStarting），只是不再由合并项触发。
		for i := 0; i < 3; i++ {
			a.mToggle.ClickedCh <- struct{}{}
		}
		waitSettled(t, renders)

		// 启停同打 commandPath，所以按 argv 分开数：启动恰好一次，
		// 而进度中的点击一次都不许被解释成「断开」。
		if n := stub.starts(); n != 1 {
			t.Errorf("启动请求打了 %d 次 (want 1)", n)
		}
		if n := stub.stops(); n != 0 {
			t.Errorf("进度中重复点击转向了断开：stop 打了 %d 次 (want 0)", n)
		}
		if st := a.fsm.State(); st != StateStarting {
			t.Errorf("状态 = %s, want Starting（进度中的重复点击不该改状态）", st)
		}
		if a.fsm.Busy() && a.fsm.CanStart() {
			t.Errorf("Busy 与 CanStart 同时为真（state=%s）", a.fsm.State())
		}
		stub.releaseStart() // 放掉扣住的启动 worker，让它落地后再收尾
		waitSettled(t, renders)
	})
}

// ── 3. 陈旧完成不得复活 deadline ────────────────────────────────

// TestConc_StaleCompletionDoesNotResurrectDeadline 复现核心场景：
// 点连接 → 启动 worker 被服务器扣住「还在飞」→ 点断开 → 停止 worker
// 设好 stopWait 的 deadline → 老启动 worker 才返回，把 deadline 重写成
// startWait（复活）；失败变体还会写 fsm.lastErr / 投递 EvStartFailed。
// 修复语义（操作序号）：陈旧完成 = 完全无副作用。
func TestConc_StaleCompletionDoesNotResurrectDeadline(t *testing.T) {
	t.Run("陈旧成功不得覆盖stop的deadline", func(t *testing.T) {
		stub := newConcStub(t)
		stub.gateStart()
		a, renders := newConcApp(t, stub.base(), &cycleProber{script: []Probe{concProbes.stopped}})
		a.refreshOnce() // → Stopped

		a.onUserStart() // 老操作：worker 扣在 connect 里
		stub.waitHits(t, commandPath, 1)

		a.onUserStop() // 新操作：Starting → Stopping，worker 打 service stop
		stub.waitHits(t, commandPath, 1)
		waitSettled(t, renders) // 停止 worker 已写入 now+stopWait 的 deadline

		releaseAt := time.Now()
		stub.releaseStart()     // 老启动 worker 此刻才拿到 success 返回
		waitSettled(t, renders) // 等它把结果写回 App（或按修复语义丢弃）

		if st := a.fsm.State(); st != StateStopping {
			t.Errorf("陈旧完成动了状态: %s, want Stopping", st)
		}
		if le := a.fsm.LastError(); le != "" {
			t.Errorf("陈旧完成写了 lastErr: %q", le)
		}
		dl := a.deadline
		if dl.IsZero() {
			t.Fatal("stop 的 deadline 被陈旧完成清掉了")
		}
		// 停止 worker 的 deadline ≈ (releaseAt-收敛耗时)+stopWait，距 releaseAt
		// 必然 ≤ stopWait；被复活成 startWait 则 ≈ +20s，远超 stopWait。
		if got := dl.Sub(releaseAt); got > stopWait+time.Second {
			t.Errorf("陈旧启动完成把 deadline 复活了：距释放 %v (> stopWait=%v，即被重写成 startWait=%v)",
				got, stopWait, startWait)
		}
	})

	t.Run("陈旧失败不得污染新操作后的状态", func(t *testing.T) {
		stub := newConcStub(t)
		stub.gateStart()
		stub.failStart("旧连接操作失败OLDOP")
		a, renders := newConcApp(t, stub.base(),
			&cycleProber{script: []Probe{concProbes.stopped, concProbes.running}})
		a.refreshOnce() // script[0]=stopped → Stopped

		a.onUserStart() // 老操作：worker 扣在 connect 里
		stub.waitHits(t, commandPath, 1)

		a.refreshOnce() // script[1]=running：Starting → Running（真实探针事实）
		waitSettled(t, renders)
		if st := a.fsm.State(); st != StateRunning {
			t.Fatalf("前置失败：state=%s, want Running", st)
		}

		a.onUserStop() // **更新的操作**：Running → Stopping，stop worker 成功 → deadline=stopWait
		stub.waitHits(t, commandPath, 1)
		waitSettled(t, renders)
		if st := a.fsm.State(); st != StateStopping {
			t.Fatalf("前置失败：state=%s, want Stopping", st)
		}

		releaseAt := time.Now()
		stub.releaseStart() // 老启动 worker 此刻才带着 OLDOP 失败返回
		waitSettled(t, renders)

		// 陈旧 = 有更新的操作在其后发生（opSeq 语义）。它的失败结果必须整个丢弃：
		// 不写 lastErr、不改状态；EvStartFailed 即便投递，从 Stopping 也无边可走，
		// 所以状态断言同时锁「不得投递出可见转移」。
		if st := a.fsm.State(); st != StateStopping {
			t.Errorf("陈旧失败把状态从 Stopping 打成 %s", st)
		}
		if le := a.fsm.LastError(); le != "" {
			t.Errorf("陈旧失败写了 lastErr=%q：用户当前操作是断开，UI 会顶着旧连接的错误", le)
		}
		if dl := a.deadline; dl.IsZero() || dl.Sub(releaseAt) > stopWait+time.Second {
			t.Errorf("陈旧失败动了 stop 的 deadline: %v（距释放 %v）", dl, dl.Sub(releaseAt))
		}
	})
}

// ── 4. 陈旧错误不得覆盖更新的错误 ───────────────────────────────

// TestConc_StaleErrorDoesNotOverwriteNewer 复现：老操作（连接）失败返回
// 的错误文案，把新操作（断开）刚写入的 lastErr 覆盖掉 —— 用户看到的是
// 上一次操作的死因，不是这一次的。
func TestConc_StaleErrorDoesNotOverwriteNewer(t *testing.T) {
	stub := newConcStub(t)
	stub.gateStart()
	stub.failStart("旧操作失败OLDOP")
	stub.failStop("新操作失败NEWOP")
	a, renders := newConcApp(t, stub.base(), &cycleProber{script: []Probe{concProbes.stopped}})
	a.refreshOnce() // → Stopped

	a.onUserStart() // 老操作：扣在 connect
	stub.waitHits(t, commandPath, 1)

	a.onUserStop() // 新操作：command 立刻业务失败 → lastErr=NEWOP, Failed
	stub.waitHits(t, commandPath, 1)
	waitSettled(t, renders)
	if le := a.fsm.LastError(); !strings.Contains(le, "NEWOP") {
		t.Fatalf("前置不成立：新操作错误没先落 (lastErr=%q)", le)
	}
	stBefore := a.fsm.State()

	stub.releaseStart() // 老操作带着 OLDOP 失败返回
	waitSettled(t, renders)

	le := a.fsm.LastError()
	if strings.Contains(le, "OLDOP") {
		t.Errorf("陈旧错误覆盖了新错误: lastErr=%q（应保留 NEWOP）", le)
	}
	if !strings.Contains(le, "NEWOP") {
		t.Errorf("新操作的错误丢了: lastErr=%q", le)
	}
	if st := a.fsm.State(); st != stBefore {
		t.Errorf("陈旧完成改了状态: %s → %s", stBefore, st)
	}
}

// ── 5. 超时判定与命令完成交错 ───────────────────────────────────

// TestConc_TimeoutInterleavesWithCompletion 复现：App 里残留一条**已过期**
// 的 deadline（上一次操作的遗留 —— playbook P2「新操作清理旧 deadline」
// 说的就是它），点击连接后 refreshOnce 的超时判定与启动 worker 的成功
// 回写并发交错：超时把状态打成 Failed+超时文案，worker 又把 deadline 设成
// now+startWait —— 既报成功又报超时。-race 同时应报 a.deadline 的读写竞争。
func TestConc_TimeoutInterleavesWithCompletion(t *testing.T) {
	stub := newConcStub(t)
	stub.setStartDelay(30 * time.Millisecond) // 拉开 worker 回写与超时判定的交错窗口
	a, renders := newConcApp(t, stub.base(), &cycleProber{script: []Probe{concProbes.stopped}})
	a.refreshOnce() // → Stopped（deadline 零值，Stopped 不 Busy，超时不触发）

	a.deadline = time.Now().Add(-time.Second) // 上一操作遗留的过期 deadline

	stop := make(chan struct{})
	waitPoll := pollLoop(a, stop, 500*time.Microsecond)

	a.onUserStart() // → Starting；30ms 后 worker 成功回写 deadline
	stub.waitHits(t, commandPath, 1)
	time.Sleep(300 * time.Millisecond) // 轮询与 worker 充分交错

	close(stop)
	waitPoll()
	waitSettled(t, renders)

	st, le, dl := a.fsm.State(), a.fsm.LastError(), a.deadline
	if strings.Contains(le, "超时") && !dl.IsZero() && dl.After(time.Now()) {
		t.Errorf("既报成功又报超时：state=%s lastErr=%q，同时成功等待的 deadline 还挂在未来 (%v)；"+
			"新操作本应清理遗留的过期 deadline", st, le, dl)
	}
	if st == StateStarting && le != "" {
		t.Errorf("Starting 却顶着错误文案: %q", le)
	}
	if !validState(st) {
		t.Errorf("状态机落到非法状态: %v", st)
	}
}

// ── 6. 退出不发 stop（回归锁，应为绿） ──────────────────────────

// TestConc_QuitDoesNotStop 锁语义：托盘退出不停隧道、不停 mynetd。
// onExit（即使与轮询并发）之后，stub 上不得出现任何请求。
func TestConc_QuitDoesNotStop(t *testing.T) {
	stub := newConcStub(t)
	a, renders := newConcApp(t, stub.base(), &cycleProber{script: []Probe{concProbes.running}})
	a.refreshOnce() // → Running：有隧道可停，才有「误停」的可能

	stop := make(chan struct{})
	waitPoll := pollLoop(a, stop, time.Millisecond)

	a.onExit() // 与轮询并发退出

	close(stop)
	waitPoll()
	waitSettled(t, renders)

	if n := stub.hits(commandPath) + stub.hits(commandPath); n != 0 {
		t.Errorf("退出路径打了 %d 个请求：托盘退出不得停隧道/停 mynetd", n)
	}
}

// ── 7. 渲染快照自洽 ─────────────────────────────────────────────

// TestConc_RenderSnapshotIsConsistent 复现：statusText 分多次读
// fsm.state / lastOneLine / fsm.lastErr，与 refreshOnce、worker 的写入
// 并发时会读到撕裂快照（标题一个状态、tooltip 的原因属于另一个状态）。
// 断言 (title, tooltip) 内部自洽；-race 应报 fsm 字段与 lastOneLine 的竞争。
func TestConc_RenderSnapshotIsConsistent(t *testing.T) {
	stub := newConcStub(t)
	svcDownMsg, nodeFailedMsg := derivedMsgs()
	// 剧本让 lastErr 反复被写/清：Running(清) → SvcDown(svcDownMsg) → Failed(nodeFailedMsg)
	a, renders := newConcApp(t, stub.base(), &cycleProber{script: []Probe{
		concProbes.running, concProbes.down, concProbes.failed,
	}})

	svcDownTitle := "状态：" + StateSvcDown.Label()
	failedTitle := "状态：" + StateFailed.Label()
	knownTitles := map[string]bool{}
	for _, st := range []State{StateStopped, StateStarting, StateRunning,
		StateDegraded, StateFailed, StateStopping, StateSvcDown} {
		knownTitles["状态："+st.Label()] = true
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // 渲染方：一致性检查
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			title, tooltip := a.statusText()
			if !strings.HasPrefix(tooltip, title) {
				t.Errorf("撕裂：tooltip=%q 不以 title=%q 开头", tooltip, title)
			}
			if !knownTitles[title] {
				t.Errorf("title 不在已知状态短语集合里: %q", title)
			}
			// 坏状态的原因文案只能配对应的标题（statusText 只在
			// Failed/SvcDown 追加 LastError；跨状态出现即为撕裂快照）
			if svcDownMsg != "" && strings.Contains(tooltip, svcDownMsg) && title != svcDownTitle {
				t.Errorf("撕裂：title=%q 却带着服务离线原因: %q", title, tooltip)
			}
			if nodeFailedMsg != "" && strings.Contains(tooltip, nodeFailedMsg) && title != failedTitle {
				t.Errorf("撕裂：title=%q 却带着节点 FAILED 原因: %q", title, tooltip)
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()
	go func() { // 点击方：制造 worker 对 fsm/lastErr 的并发写
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				a.onUserStart()
			} else {
				a.onUserStop()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	waitPoll := pollLoop(a, stop, 300*time.Microsecond) // 轮询方

	time.Sleep(400 * time.Millisecond)
	close(stop)
	wg.Wait()
	waitPoll()
	waitSettled(t, renders)

	if st := a.fsm.State(); !validState(st) {
		t.Errorf("状态机落到非法状态: %v", st)
	}
}

// ── 8. 原生 UI 推送顺序：最新胜出 ────────────────────────────────

// TestConc_PushOrderLatestWins 锁 acceptPush 的顺序判定。
//
// 为什么单测这个方法而不是 push：push 里的 systray.SetTooltip/SetTitle 在
// darwin 是无条件 cgo，没有事件循环会崩（文件头纪律①），所以「慢快照不得覆盖
// 新状态」这条只能脱离 cgo 验证。可达性是真的：render() = push(snapshot())，
// snapshot 用 mu、push 用 renderMu，两把锁之间可被别的 goroutine 插入。
func TestConc_PushOrderLatestWins(t *testing.T) {
	t.Run("旧序号在已推新序号之后必须被拒", func(t *testing.T) {
		a := &App{}
		if !a.acceptPush(1) {
			t.Error("seq=1 首次推送应被接受")
		}
		if !a.acceptPush(5) {
			t.Error("seq=5 是更新的快照，应被接受")
		}
		if a.acceptPush(3) {
			t.Error("seq=3 比已推的 5 旧，必须拒绝 —— 否则界面退回旧状态")
		}
		if a.acceptPush(2) {
			t.Error("seq=2 同样必须拒绝")
		}
		if !a.acceptPush(5) {
			t.Error("等序号应被接受（幂等重推）")
		}
		if !a.acceptPush(6) {
			t.Error("seq=6 更新，应被接受")
		}
	})

	t.Run("并发推送收敛到最大序号且无重复接受", func(t *testing.T) {
		a := &App{}
		const goroutines, per = 8, 200
		var mu sync.Mutex
		seen := make(map[uint64]int, goroutines*per)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				<-start
				for i := 1; i <= per; i++ {
					seq := uint64(g*per + i)
					if a.acceptPush(seq) {
						mu.Lock()
						seen[seq]++
						mu.Unlock()
					}
				}
			}(g)
		}
		close(start)
		wg.Wait()

		// ⚠️ 这里**不能**断言「被接受序号严格递增」。记录发生在 acceptPush
		// 返回之后、在测试自己的 mu 里，所以切片顺序 ≠ 接受顺序：
		// 接受 202 要求当时 pushedSeq ≤ 202（即 601 尚未被接受），但 601 的
		// 那次 append 完全可能因为调度而排在 202 前面。实测 -count=3 就会
		// 偶发报「601 → 202」。要在锁内记录顺序就得把测试钩子塞进产品代码，
		// 不值得 —— 顺序语义由上一个子测试确定性覆盖，本子测试的职责是给
		// race 检测器制造 renderMu/pushedSeq 上的真实并发流量。
		if len(seen) == 0 {
			t.Fatal("没有任何推送被接受")
		}
		for seq, n := range seen {
			if n != 1 {
				t.Errorf("seq=%d 被接受了 %d 次（每个序号只投一次，不该重复）", seq, n)
			}
		}
		if got := a.pushedSeq; got != goroutines*per {
			t.Errorf("pushedSeq=%d, want %d（最大序号丢失 ⇒ 守卫存在丢更新）", got, goroutines*per)
		}
		t.Logf("并发 %d×%d 次推送，%d 个序号被接受，收敛到 %d",
			goroutines, per, len(seen), a.pushedSeq)
	})
}
