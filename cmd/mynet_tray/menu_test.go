package main

// P4 的菜单面：合并后的连接/断开、复制虚拟 IP、文案。
//
// 判据一律**从 FSM 取**，不在测试里手抄一遍 CanStart/CanStop/Busy ——
// 抄一遍就等于承认第二套判据存在（§3「同义判据多份必漂」）。

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
)

// fsmPaths 把 FSM 摆到目标态的事件序列。走真实转移表，不直接改字段。
var fsmPaths = map[State][]Event{
	StateSvcDown:  nil, // NewFSM 的初值
	StateStopped:  {EvDaemonDown},
	StateStarting: {EvDaemonDown, EvStart},
	StateRunning:  {EvDaemonDown, EvNodeHealthy},
	StateDegraded: {EvDaemonDown, EvNodeHealthy, EvNodeDegraded},
	StateFailed:   {EvDaemonDown, EvNodeHealthy, EvNodeFailed},
	StateStopping: {EvDaemonDown, EvNodeHealthy, EvStop},
}

func fsmInState(t *testing.T, st State) *FSM {
	t.Helper()
	f := NewFSM()
	for _, ev := range fsmPaths[st] {
		f.On(ev)
	}
	if f.State() != st {
		t.Fatalf("摆不出目标态：得到 %s, want %s", f.State(), st)
	}
	return f
}

// TestToggleForCoversEveryState 合并项在**每一个**状态上的文案与可点性。
//
// 两条要锁住的性质：
//   - 过渡态显示状态机自己的进行时文案并禁用（进度期间不许再发写操作）；
//   - Failed 同时满足 CanStart 与 CanStop，取「连接」（重试优先）。
func TestToggleForCoversEveryState(t *testing.T) {
	want := map[State]struct {
		title   string
		enabled bool
	}{
		StateStopped:  {"连接隧道", true},
		StateFailed:   {"连接隧道", true},
		StateRunning:  {"断开隧道", true},
		StateDegraded: {"断开隧道", true},
		StateStarting: {"正在连接…", false},
		StateStopping: {"正在断开…", false},
		StateSvcDown:  {"连接隧道", false},
	}
	if len(want) != len(fsmPaths) {
		t.Fatalf("用例覆盖了 %d 个状态，FSM 有 %d 个", len(want), len(fsmPaths))
	}
	for st, w := range want {
		t.Run(st.String(), func(t *testing.T) {
			f := fsmInState(t, st)
			title, enabled := toggleFor(f.State(), f.CanStart(), f.CanStop(), f.Busy())
			if title != w.title || enabled != w.enabled {
				t.Errorf("toggleFor(%s) = (%q, %v), want (%q, %v)", st, title, enabled, w.title, w.enabled)
			}
		})
	}
}

// TestSnapshotToggleSameSourceAsFSM 菜单文案必须和状态行同源（都出自 snapshot），
// 否则会出现「状态行说已连接、按钮写着连接隧道」这种自相矛盾的界面。
func TestSnapshotToggleSameSourceAsFSM(t *testing.T) {
	cases := []struct {
		name    string
		pr      Probe
		title   string
		enabled bool
	}{
		{"running", bootProbes["running"], "断开隧道", true},
		{"stopped", bootProbes["stopped"], "连接隧道", true},
		{"down", bootProbes["down"], "连接隧道", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _ := newBootApp(t, c.pr)
			a.bootSequence()
			v := a.snapshot()
			if v.toggleTitle != c.title || v.toggleEnabled != c.enabled {
				t.Errorf("snapshot 的合并项 = (%q, %v), want (%q, %v)",
					v.toggleTitle, v.toggleEnabled, c.title, c.enabled)
			}
			// 状态行与按钮不得互相打脸
			if c.enabled && !strings.Contains(v.title, "状态：") {
				t.Errorf("状态行 = %q，看不出是状态行", v.title)
			}
		})
	}
}

// TestOnUserToggleDecidesFromState 同一个点击入口按当前态决定连接还是断开。
// 进度中（Busy）必须是彻底的 no-op：既不重发 connect，也不把点击解释成断开。
func TestOnUserToggleDecidesFromState(t *testing.T) {
	failedProbe := Probe{Up: true, Valid: true, NodeState: "FAILED", ProcessUp: true, OneLine: "节点失败"}

	cases := []struct {
		name        string
		pr          Probe
		prep        func(t *testing.T, a *App) // 额外把 App 摆到目标态
		wantConnect int
		wantCommand int
		wantState   State
	}{
		{"停着点一下是连接", bootProbes["stopped"], nil, 1, 0, StateStarting},
		{"跑着点一下是断开", bootProbes["running"], nil, 0, 1, StateStopping},
		{"服务离线点一下什么都不发", bootProbes["down"], nil, 0, 0, StateSvcDown},
		{"连接异常点一下是重试", failedProbe, nil, 1, 0, StateStarting},
		{"进度中再点是no-op", bootProbes["stopped"], func(t *testing.T, a *App) {
			a.onUserToggle() // → Starting，worker 在飞
			waitWorkerLanded(t, a)
		}, 1, 0, StateStarting},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, stub := newBootApp(t, c.pr)
			a.refreshOnce()
			if c.prep != nil {
				c.prep(t, a)
			}
			before := a.fsm.State()

			a.onUserToggle()
			if c.wantConnect > 0 || c.wantCommand > 0 {
				waitWorkerLanded(t, a)
			}

			if n := len(stub.hits(connectPath)); n != c.wantConnect {
				t.Errorf("connect 打了 %d 次, want %d", n, c.wantConnect)
			}
			if n := len(stub.hits(commandPath)); n != c.wantCommand {
				t.Errorf("command 打了 %d 次, want %d", n, c.wantCommand)
			}
			if got := a.fsm.State(); got != c.wantState {
				t.Errorf("状态 = %s, want %s（点击前是 %s）", got, c.wantState, before)
			}
		})
	}
}

// ── P4：复制虚拟 IP ───────────────────────────────────────────

func TestValidOverlayIP(t *testing.T) {
	cases := []struct{ in, want string }{
		{"10.200.0.17", "10.200.0.17"},
		{"  10.200.0.17 ", "10.200.0.17"},
		{"2001:0db8:0000:0000:0000:0000:0000:0001", "2001:db8::1"}, // 规范化后才进剪贴板
		{"", ""},
		{"10.200.0.999", ""},
		{"not-an-ip", ""},
		{"私钥指纹 0123456789abcdef0123456789abcdef", ""}, // 上游脏值不许透出去
		{"10.200.0.17 extra", ""},
		// 首尾空白是配置文件解析的常见残留：容忍，但规范化掉 ——
		// 剪贴板里绝不能带一个看不见的换行。
		{"10.200.0.17\n", "10.200.0.17"},
	}
	for _, c := range cases {
		if got := validOverlayIP(c.in); got != c.want {
			t.Errorf("validOverlayIP(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCopyOverlayIP 点击才写剪贴板，成败都上屏，且上屏文案不含底层 error。
func TestCopyOverlayIP(t *testing.T) {
	withIP := Probe{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: true,
		OneLine: "运行中", OverlayIP: "10.200.0.17"}

	t.Run("成功", func(t *testing.T) {
		a, _ := newBootApp(t, withIP)
		var got []string
		a.clipFn = func(s string) error { got = append(got, s); return nil }
		a.refreshOnce()

		a.copyOverlayIP()

		if len(got) != 1 || got[0] != "10.200.0.17" {
			t.Fatalf("写进剪贴板的是 %v, want [10.200.0.17]", got)
		}
		_, tooltip := a.statusText()
		if !strings.Contains(tooltip, "已复制虚拟 IP 10.200.0.17") {
			t.Errorf("成功必须上屏，tooltip = %q", tooltip)
		}
		if v := a.snapshot().overlayIP; v != "10.200.0.17" {
			t.Errorf("snapshot.overlayIP = %q，菜单项该显示", v)
		}
	})

	t.Run("失败要上屏且不泄漏底层错误", func(t *testing.T) {
		a, _ := newBootApp(t, withIP)
		a.clipFn = func(string) error {
			return errors.New("exec: \"pbcopy\": executable file not found in $PATH")
		}
		a.refreshOnce()

		a.copyOverlayIP()

		_, tooltip := a.statusText()
		if !strings.Contains(tooltip, "复制到剪贴板失败") {
			t.Errorf("失败必须上屏，tooltip = %q", tooltip)
		}
		if strings.Contains(tooltip, "pbcopy") || strings.Contains(tooltip, "$PATH") {
			t.Errorf("上屏文案带了底层 error 原文（D20）：%q", tooltip)
		}
	})

	t.Run("缺失就不显示也不写", func(t *testing.T) {
		a, _ := newBootApp(t, bootProbes["running"]) // 无 overlay_ip
		calls := 0
		a.clipFn = func(string) error { calls++; return nil }
		a.refreshOnce()

		if v := a.snapshot().overlayIP; v != "" {
			t.Errorf("snapshot.overlayIP = %q, want 空（菜单项该隐藏）", v)
		}
		a.copyOverlayIP() // 正常点不到，真收到也要如实说没有
		if calls != 0 {
			t.Errorf("没有 IP 却调了 %d 次剪贴板", calls)
		}
		if _, tooltip := a.statusText(); !strings.Contains(tooltip, "当前没有可复制的虚拟 IP") {
			t.Errorf("tooltip = %q", tooltip)
		}
	})

	t.Run("非法值当缺失处理", func(t *testing.T) {
		bad := withIP
		bad.OverlayIP = "10.200.0.999"
		a, _ := newBootApp(t, bad)
		calls := 0
		a.clipFn = func(string) error { calls++; return nil }
		a.refreshOnce()

		if v := a.snapshot().overlayIP; v != "" {
			t.Errorf("非法 overlay_ip 不得进 UI，got %q", v)
		}
		a.copyOverlayIP()
		if calls != 0 {
			t.Errorf("非法值却写进了剪贴板（%d 次）", calls)
		}
	})
}

// TestPollingNeverTouchesClipboard 轮询本身绝不写剪贴板：只有点击才写。
// 这条是「仅用户点击写剪贴板」的行为落点 —— 探针每 3s 一轮，
// 若哪天顺手在渲染路径里也复制一次，用户剪贴板就会被无声覆盖。
func TestPollingNeverTouchesClipboard(t *testing.T) {
	pr := Probe{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: true, OverlayIP: "10.200.0.17"}
	a, _ := newBootApp(t, pr)
	calls := 0
	a.clipFn = func(string) error { calls++; return nil }

	for i := 0; i < 3; i++ {
		a.refreshOnce()
	}

	if calls != 0 {
		t.Errorf("轮询 %d 轮却调了 %d 次剪贴板", 3, calls)
	}
	if v := a.snapshot().overlayIP; v != "10.200.0.17" {
		t.Errorf("IP 该照常进快照供菜单显示，got %q", v)
	}
}

// TestOverlayIPFollowsLatestProbe 菜单上的 IP 必须跟着**最近一次**探针走。
//
// 两个方向都要锁：
//   - 冷启动那次探针就得把 IP 带进快照（用户登录完第一件事往往就是复制它，
//     不该等 3s 轮询才出现）；
//   - 下一轮探针没带 IP 时必须**清空**。overlay_ip 出自配置面，换节点/改配置后
//     旧值还在的话，用户复制到的就是一个不属于当前节点的地址（§8.1「不复制旧 IP」）。
func TestOverlayIPFollowsLatestProbe(t *testing.T) {
	withIP := Probe{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: true, OverlayIP: "10.200.0.17"}
	noIP := Probe{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: true}

	t.Run("冷启动就带上", func(t *testing.T) {
		a, _ := newBootApp(t, withIP)
		a.bootSequence()
		if v := a.snapshot().overlayIP; v != "10.200.0.17" {
			t.Errorf("冷启动后 snapshot.overlayIP = %q, want 10.200.0.17", v)
		}
	})

	t.Run("探针不再报就清空", func(t *testing.T) {
		stub := newStub(t, stubSpec{connectFile: "connect_ok.json", commandFile: "command_ok.json"})
		a, _ := testApp(t, stub.base(), &fakeProber{script: []Probe{withIP, noIP}})
		a.refreshOnce()
		if v := a.snapshot().overlayIP; v != "10.200.0.17" {
			t.Fatalf("第一轮后 = %q, want 10.200.0.17", v)
		}
		a.refreshOnce()
		if v := a.snapshot().overlayIP; v != "" {
			t.Errorf("第二轮探针没带 IP，却还留着 %q（旧 IP 不许残留）", v)
		}
	})
}

// TestDiagURLMatchesWebUIPage 「查看诊断」的深链必须真的对得上 WebUI 的页面键。
//
// 这条跨包读源文件是有意的：#diag 写错、或 WebUI 哪天改了页面键，都不会报错，
// 只会静默退回概览页 —— 用户点了「查看诊断」却看不到诊断，且无从察觉。
func TestDiagURLMatchesWebUIPage(t *testing.T) {
	const webui = "../../mynetd/localapi/webui/index.html"
	raw, err := os.ReadFile(webui)
	if err != nil {
		t.Fatalf("读 WebUI 源文件（%s）: %v", webui, err)
	}
	// 两件事缺一深链就不成立：PAGES 里有 diag 这一页，且启动时真的解析 hash。
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`diag:\s*\{\s*el:\s*'page-diag'`),
		regexp.MustCompile(`\nparseHash\(\);`),
	} {
		if !re.Match(raw) {
			t.Errorf("WebUI 里找不到 %s —— #diag 深链已失效，DiagURL 得跟着改", re)
		}
	}

	const base = "127.0.0.1:8096"
	if got, want := DiagURL(base), "http://127.0.0.1:8096/ui/#diag"; got != want {
		t.Errorf("DiagURL = %q, want %q", got, want)
	}
	if !strings.HasPrefix(DiagURL(base), PanelURL(base)) {
		t.Error("DiagURL 必须基于 PanelURL 拼，否则两处地址会各自漂")
	}
}

// TestAcceptCopyIPOnlyOnChange 显隐只在值变了才推 systray（同 acceptIcon）。
func TestAcceptCopyIPOnlyOnChange(t *testing.T) {
	a, _ := newBootApp(t, bootProbes["stopped"])

	if a.acceptCopyIP("") {
		t.Error("冷启动 pushedCopyIP 零值就是「隐藏」，重复推空串必须被拦下")
	}
	if !a.acceptCopyIP("10.200.0.17") {
		t.Error("第一次拿到 IP 必须放行（否则菜单项永远不显示）")
	}
	if a.acceptCopyIP("10.200.0.17") {
		t.Error("同值重复推必须被拦下")
	}
	if !a.acceptCopyIP("") {
		t.Error("IP 消失必须放行（要隐藏）")
	}
}
