package main

// P3 的反馈面与意图落盘测试（docs/TRAY_OPTIMIZATION_PLAYBOOK.md §P3）。
//
// 锁四件事：
//   - D9  意图文件落在登录用户可写目录，能存能重读，格式仍是 want_node；
//         写失败必须**上屏**，不许 `_ =` 静默丢掉（旧版就是这么失效的）。
//   - P3:114 一次性提示要**留得住**：存进状态由渲染路径带上，
//         而不是直接 SetTooltip（那样下一轮 3s 探针立刻冲掉）。
//   - D10/D11 自启菜单文案来自真实状态；读不到就是「状态未知」，
//         不用「未勾选」冒充「已关闭」；切换失败可见。
//
// 一律不碰 systray 原生调用（darwin 腿无条件 cgo）：断言走 statusText()
// 与 snapshot()，两者都只在 mu 内算文本。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// isolateUserConfig 把 os.UserConfigDir()/os.UserHomeDir() 关进临时目录，
// 免得测试写真实用户配置目录（也顺带隔离 launchAgentStarter 的 plist 路径）。
// 三个变量分别对应 darwin / windows / linux 的取值来源。
//
// ⚠️ testApp() 内部也会调它，且每次都换一个新 TempDir —— 所以任何依赖
// 「已经布置好的 HOME」的动作（breakIntentDir、预写 intent 文件）必须排在
// testApp() **之后**，否则会被它换掉的 HOME 一起作废。
func isolateUserConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AppData", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

// breakIntentDir 把意图目录本身做成一个普通文件，于是 MkdirAll 必然失败 ——
// 用来验「写不进去时用户看得见」，而不是靠改产品代码注入错误。
func breakIntentDir(t *testing.T) {
	t.Helper()
	dir, err := intentDir()
	if err != nil {
		t.Fatalf("intentDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatalf("准备父目录: %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatalf("占位文件: %v", err)
	}
}

// ── D9：意图落盘 ───────────────────────────────────────────────

func TestIntentRoundTrip(t *testing.T) {
	isolateUserConfig(t)

	if got := loadIntent(); got.WantNode {
		t.Fatalf("空目录应读成默认 false，got %+v", got)
	}
	if err := saveIntent(trayIntent{WantNode: true}); err != nil {
		t.Fatalf("可写目录下 saveIntent 不该失败: %v", err)
	}
	if got := loadIntent(); !got.WantNode {
		t.Errorf("重读 = %+v, want want_node=true", got)
	}

	// 键名是跨版本合同：改了就等于把已有用户的意图全部作废。
	raw, err := os.ReadFile(mustIntentPath(t))
	if err != nil {
		t.Fatalf("%v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("意图文件不是合法 JSON: %v", err)
	}
	if v, ok := m["want_node"]; !ok || v != true {
		t.Errorf("want_node 键缺失或值不对: %s", raw)
	}
	if len(m) != 1 {
		t.Errorf("意图文件多出了未声明的字段（D9 要求无 schema 漂移）: %s", raw)
	}

	if err := saveIntent(trayIntent{WantNode: false}); err != nil {
		t.Fatalf("%v", err)
	}
	if got := loadIntent(); got.WantNode {
		t.Errorf("覆盖写没生效: %+v", got)
	}
}

func TestIntentFileIsUserPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的 0600 映射成只读属性而非 ACL，断言无意义")
	}
	isolateUserConfig(t)
	if err := saveIntent(trayIntent{WantNode: true}); err != nil {
		t.Fatalf("%v", err)
	}

	dirInfo, err := os.Stat(filepath.Dir(mustIntentPath(t)))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("目录权限 = %o, want 700", perm)
	}
	fileInfo, err := os.Stat(mustIntentPath(t))
	if err != nil {
		t.Fatalf("%v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("文件权限 = %o, want 600", perm)
	}
}

func TestLoadIntentToleratesGarbage(t *testing.T) {
	isolateUserConfig(t)
	p := mustIntentPath(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("%v", err)
	}
	for _, body := range []string{"", "not json", `{"want_node":"yes"}`, `{"other":1}`} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("%v", err)
		}
		if got := loadIntent(); got.WantNode {
			t.Errorf("坏文件 %q 读成了 %+v，应回落默认 false", body, got)
		}
	}
}

func TestSaveIntentSurfacesWriteFailure(t *testing.T) {
	isolateUserConfig(t)
	breakIntentDir(t)
	if err := saveIntent(trayIntent{WantNode: true}); err == nil {
		t.Fatal("目录被占位文件挡住时 saveIntent 应返回错误（旧版把错误 `_ =` 丢掉）")
	}
}

// TestIntentSaveFailureReachesUser 是 D9 的出口断言：写失败不能只是
// saveIntent 返回个 error 就完事 —— 用户点「连接隧道」，托盘必须告诉他
// 「重启后不会自动恢复」。
func TestIntentSaveFailureReachesUser(t *testing.T) {
	isolateUserConfig(t)
	stub := newStub(t, stubSpec{connectFile: "connect_ok.json", commandFile: "command_ok.json"})
	a, _ := testApp(t, stub.base(), &fakeProber{script: []Probe{
		{Up: true, Valid: true, NodeState: "STOPPED", OneLine: "已停止"},
	}})
	a.refreshOnce()
	if !a.fsm.CanStart() {
		t.Fatalf("前置：状态 %s 应当可启动", a.fsm.State())
	}
	// 必须在 testApp 之后再堵：testApp 自己会 isolateUserConfig，
	// 换掉 HOME 就把这里做的破坏一起作废了。
	breakIntentDir(t)

	a.onUserStart()
	waitWorkerLanded(t, a)

	_, tooltip := a.statusText()
	if !strings.Contains(tooltip, "意图保存失败") {
		t.Errorf("意图写失败没有上屏，tooltip = %q", tooltip)
	}
	if len(stub.hits(connectPath)) != 1 {
		t.Errorf("意图写失败不该拦住连接本身")
	}
}

// waitWorkerLanded 等 runStart 落完再断言。判据取 mu 保护的 deadline：
// worker 成功路径的最后一步就是在 applyOpResult 里写它，写到了就说明
// worker 已结束，测试与它（以及它那次 HTTP 往返）之间有了 happens-before。
//
// 不用 renders 计数当判据：testApp 的 renderFn 是裸 `renders++`，
// 跨 goroutine 读它自己就是 race（concurrency_test 那套用的是 atomic）。
// 同理，apiStub.reqs 也没有锁 —— 只能在这之后读，不能在等待期间轮询。
func waitWorkerLanded(t *testing.T, a *App) {
	t.Helper()
	limit := time.Now().Add(3 * time.Second)
	for time.Now().Before(limit) {
		a.mu.Lock()
		d := a.deadline
		a.mu.Unlock()
		if !d.IsZero() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker 没收敛：deadline 一直没被写")
}

func mustIntentPath(t *testing.T) string {
	t.Helper()
	p, err := intentPath()
	if err != nil {
		t.Fatalf("intentPath: %v", err)
	}
	return p
}

// ── P3:114：一次性提示要留得住 ─────────────────────────────────

func TestNoticeSurvivesProbeRefresh(t *testing.T) {
	isolateUserConfig(t)
	a, renders := testApp(t, "127.0.0.1:1", &fakeProber{script: []Probe{
		{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: true, OneLine: "运行中"},
	}})
	a.refreshOnce()

	const msg = "开启开机启动失败"
	a.notice(msg)
	before := *renders

	// 旧版直接 SetTooltip：下一次探针渲染就把提示冲掉，用户点了却什么都看不到。
	a.refreshOnce()
	if *renders <= before {
		t.Fatal("前置：refreshOnce 应当真的重画了一次")
	}
	if _, tooltip := a.statusText(); !strings.Contains(tooltip, msg) {
		t.Errorf("提示被探针冲掉了，tooltip = %q", tooltip)
	}
}

func TestNoticeExpires(t *testing.T) {
	isolateUserConfig(t)
	a, _ := testApp(t, "127.0.0.1:1", &fakeProber{})

	a.notice("过期提示")
	if _, tooltip := a.statusText(); !strings.Contains(tooltip, "过期提示") {
		t.Fatalf("刚发的提示就该在，tooltip = %q", tooltip)
	}

	a.mu.Lock()
	a.noticeUntil = time.Now().Add(-time.Second)
	a.mu.Unlock()

	if _, tooltip := a.statusText(); strings.Contains(tooltip, "过期提示") {
		t.Errorf("过期提示赖在 tooltip 上冒充现状: %q", tooltip)
	}
}

// 状态行是常显面，提示不得把 one_line 与失败原因挤掉。
func TestNoticeCoexistsWithStatus(t *testing.T) {
	isolateUserConfig(t)
	a, _ := testApp(t, "127.0.0.1:1", &fakeProber{script: []Probe{
		{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: true, OneLine: "运行中"},
	}})
	a.refreshOnce()
	a.notice("关闭开机启动失败")

	_, tooltip := a.statusText()
	for _, want := range []string{"关闭开机启动失败", "运行中"} {
		if !strings.Contains(tooltip, want) {
			t.Errorf("tooltip 缺 %q: %q", want, tooltip)
		}
	}
	if !strings.HasPrefix(tooltip, "状态：") {
		t.Errorf("提示不该挤掉状态短语: %q", tooltip)
	}
}

// ── D10/D11：自启菜单文案 ──────────────────────────────────────

func TestAutoTitleFor(t *testing.T) {
	boom := errors.New("launchctl 查询失败")
	cases := []struct {
		name string
		on   bool
		err  error
		want string
	}{
		{"开着", true, nil, "开机启动 ✓"},
		{"关着", false, nil, "开机启动"},
		// 读不到状态时 err 压过 on：那才是真正的「未知」，
		// 显示成「已关闭」是个未经验证的结论。
		{"查询失败", false, boom, autoTitleUnknown},
		{"查询失败但曾开着", true, boom, autoTitleUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := autoTitleFor(c.on, c.err); got != c.want {
				t.Errorf("autoTitleFor(%v, %v) = %q, want %q", c.on, c.err, got, c.want)
			}
		})
	}
}

// 冷启动还没查过，菜单不得先断言「已关闭」。
func TestAutoTitleUnknownBeforeSync(t *testing.T) {
	isolateUserConfig(t)
	a, _ := testApp(t, "127.0.0.1:1", &fakeProber{})
	if got := a.snapshot().autoTitle; got != autoTitleUnknown {
		t.Errorf("未同步时 autoTitle = %q, want %q", got, autoTitleUnknown)
	}
}

// fakeAutoStarter 复刻真实 starter 的合同：
//   - sys = 安装器已在系统级登记（/Library/LaunchAgents、HKLM Run）；
//   - IsEnabled = 系统级或用户级任一份在位；
//   - Enable 在 sys 时直接拒绝（不许造出第二份）；
//   - Disable 先清掉用户级那份，再报系统级仍在位（否则双托盘留着、
//     且用户以为自己关掉了）。
type fakeAutoStarter struct {
	on         bool
	sys        bool
	isErr      error
	enableErr  error
	disableErr error
	isCalls    int
	enCalls    int
	disCalls   int
}

func (f *fakeAutoStarter) IsEnabled() (bool, error) {
	f.isCalls++
	return f.on || f.sys, f.isErr
}

func (f *fakeAutoStarter) Enable() error {
	f.enCalls++
	if f.sys {
		return errSystemManaged
	}
	if f.enableErr != nil {
		return f.enableErr
	}
	f.on = true
	return nil
}

func (f *fakeAutoStarter) Disable() error {
	f.disCalls++
	if f.disableErr != nil {
		return f.disableErr
	}
	f.on = false
	if f.sys {
		return errSystemManaged
	}
	return nil
}

// TestToggleAutoStartRefusesWhenInstallerRegistered 锁 D10/D11 的用户可见症状：
// 安装器登记在位时，菜单必须显示「已启用」（旧版只看用户级 ⇒ 一直显示未勾选），
// 点击必须**说明为什么改不了**，而且不许把菜单翻成「未勾选」骗用户说关掉了。
func TestToggleAutoStartRefusesWhenInstallerRegistered(t *testing.T) {
	isolateUserConfig(t)
	a, _ := testApp(t, "127.0.0.1:1", &fakeProber{})
	st := &fakeAutoStarter{sys: true}
	a.autoStart = st
	a.syncAutoMenu()
	if got := a.snapshot().autoTitle; got != "开机启动 ✓" {
		t.Fatalf("安装器登记在位时菜单应显示已启用，got %q", got)
	}

	a.toggleAutoStart()

	if st.enCalls != 0 {
		t.Errorf("系统级已在位就不许再 Enable（会造出同名第二份）：Enable=%d", st.enCalls)
	}
	if _, tooltip := a.statusText(); !strings.Contains(tooltip, errSystemManaged.Error()) {
		t.Errorf("没告诉用户为什么改不了，tooltip = %q", tooltip)
	}
	if got := a.snapshot().autoTitle; got != "开机启动 ✓" {
		t.Errorf("菜单翻成了 %q —— 系统级那份还在，自启并没有关掉", got)
	}
}

func TestToggleAutoStartReportsFailure(t *testing.T) {
	boom := errors.New("denied")
	cases := []struct {
		name       string
		starter    *fakeAutoStarter
		wantNotice string
		wantTitle  string
	}{
		{
			// 读不到就别猜着切：按 err 当「关着」去 Enable，可能把已经开着的
			// 再开一遍，且失败对用户完全不可见。
			name:       "查询失败不动手",
			starter:    &fakeAutoStarter{on: true, isErr: boom},
			wantNotice: "读取开机启动状态失败",
			wantTitle:  autoTitleUnknown,
		},
		{
			name:       "开启失败",
			starter:    &fakeAutoStarter{enableErr: boom},
			wantNotice: "开启开机启动失败",
			wantTitle:  "开机启动",
		},
		{
			name:       "关闭失败",
			starter:    &fakeAutoStarter{on: true, disableErr: boom},
			wantNotice: "关闭开机启动失败",
			wantTitle:  "开机启动 ✓",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isolateUserConfig(t)
			a, _ := testApp(t, "127.0.0.1:1", &fakeProber{})
			a.autoStart = c.starter
			a.syncAutoMenu()

			a.toggleAutoStart()

			_, tooltip := a.statusText()
			if !strings.Contains(tooltip, c.wantNotice) {
				t.Errorf("失败没有上屏，tooltip = %q, want 含 %q", tooltip, c.wantNotice)
			}
			if got := a.snapshot().autoTitle; got != c.wantTitle {
				t.Errorf("失败后菜单文案 = %q, want %q（失败不许伪装成已切换）", got, c.wantTitle)
			}
		})
	}

	t.Run("查询失败时不得试探性切换", func(t *testing.T) {
		isolateUserConfig(t)
		a, _ := testApp(t, "127.0.0.1:1", &fakeProber{})
		st := &fakeAutoStarter{isErr: boom}
		a.autoStart = st

		a.toggleAutoStart()

		if st.enCalls != 0 || st.disCalls != 0 {
			t.Errorf("读不到状态还动手了：Enable=%d Disable=%d", st.enCalls, st.disCalls)
		}
	})
}

func TestToggleAutoStartSuccessRereadsState(t *testing.T) {
	isolateUserConfig(t)
	a, _ := testApp(t, "127.0.0.1:1", &fakeProber{})
	st := &fakeAutoStarter{}
	a.autoStart = st
	a.syncAutoMenu()
	if got := a.snapshot().autoTitle; got != "开机启动" {
		t.Fatalf("前置 autoTitle = %q", got)
	}

	a.toggleAutoStart()

	if st.enCalls != 1 || st.disCalls != 0 {
		t.Fatalf("关着时点一次应只 Enable 一次：Enable=%d Disable=%d", st.enCalls, st.disCalls)
	}
	// 菜单显示的是**重读后的真实状态**，不是「我们刚请求的意图」。
	if got := a.snapshot().autoTitle; got != "开机启动 ✓" {
		t.Errorf("切换成功后 autoTitle = %q, want %q", got, "开机启动 ✓")
	}
	if _, tooltip := a.statusText(); strings.Contains(tooltip, "失败") {
		t.Errorf("成功路径不该带失败提示: %q", tooltip)
	}

	a.toggleAutoStart()
	if st.disCalls != 1 {
		t.Fatalf("再点一次应 Disable：Disable=%d", st.disCalls)
	}
	if got := a.snapshot().autoTitle; got != "开机启动" {
		t.Errorf("关闭后 autoTitle = %q", got)
	}
}

// syncAutoMenu 之后菜单必须拿到真实值，不能一直挂着「状态未知」。
func TestSyncAutoMenuReplacesUnknown(t *testing.T) {
	isolateUserConfig(t)
	a, renders := testApp(t, "127.0.0.1:1", &fakeProber{})
	before := *renders

	a.autoStart = &fakeAutoStarter{on: true}
	a.syncAutoMenu()

	if got := a.snapshot().autoTitle; got != "开机启动 ✓" {
		t.Errorf("autoTitle = %q", got)
	}
	if *renders <= before {
		t.Error("syncAutoMenu 应触发一次渲染")
	}
}
