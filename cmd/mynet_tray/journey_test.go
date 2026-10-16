package main

// P5 回归：一条完整的用户旅程。
//
// 合并启停项、状态图标、复制虚拟 IP 三者在各自的单测里都验过
// （menu_test.go / icon_test.go），但没有任何用例把它们串在同一条时间线上走一遍。
// 这三个面都由 snapshot 一处产出，一旦有人只改其中一个的判据，
// 单独测仍然全绿，用户却会看到「图标说连着、按钮写着连接隧道」这种自相矛盾。
// 这里锁的是**同时成立**：每一步都对状态、按钮、图标、可复制 IP 四项断言。

import (
	"sync"
	"testing"
)

// stepProber 由测试在确定的时刻换掉探针事实。
// cycleProber 是循环剧本（并发用例要的是「反复变」），旅程要的是「我说变才变」，
// 所以另用一个可写的替身，而不是给 cycleProber 加模式开关。
type stepProber struct {
	mu  sync.Mutex
	cur Probe
}

func (p *stepProber) set(pr Probe) {
	p.mu.Lock()
	p.cur = pr
	p.mu.Unlock()
}

func (p *stepProber) Probe() (Probe, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur, nil
}

func TestJourney_MenuStaysCoherent(t *testing.T) {
	stub := newStub(t, stubSpec{connectFile: "connect_ok.json", commandFile: "command_ok.json"})
	pr := &stepProber{}
	a, renders := newConcApp(t, stub.base(), pr)
	a.autoStart = &fakeAutoStarter{}

	const ip = "10.200.0.17"
	// overlay_ip 是**配置面**派生值：node_status.go:522-538 只看 route.conf /
	// intent / mynet.conf / bundle 对端行，与 State、ProcessUp 无关。
	// 所以只要节点配置还在盘上，停着/失败都照样报同一个 IP —— 旅程里的
	// 节点面探针一律带上它；唯一不带的是「本机服务不可用」（那时什么都读不到）。
	var (
		down    = Probe{Up: false}
		stopped = Probe{Up: true, Valid: true, NodeState: "STOPPED", OneLine: "已停止", OverlayIP: ip}
		running = Probe{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: true, OneLine: "运行中", OverlayIP: ip}
		failed  = Probe{Up: true, Valid: true, NodeState: "FAILED", ProcessUp: true, OneLine: "节点失败", OverlayIP: ip}
	)

	// want 是旅程某一步上四个面必须同时成立的值。
	type want struct {
		state   State
		title   string
		enabled bool
		icon    iconKind
		ip      string // "" = 「复制虚拟 IP」必须隐藏
	}
	check := func(step string, w want) {
		t.Helper()
		waitSettled(t, renders)
		v := a.snapshot()
		if got := a.fsm.State(); got != w.state {
			t.Errorf("%s：状态 = %s, want %s", step, got, w.state)
		}
		if v.toggleTitle != w.title || v.toggleEnabled != w.enabled {
			t.Errorf("%s：合并项 = (%q, %v), want (%q, %v)", step, v.toggleTitle, v.toggleEnabled, w.title, w.enabled)
		}
		if v.icon != w.icon {
			t.Errorf("%s：图标 = %d, want %d", step, v.icon, w.icon)
		}
		if v.overlayIP != w.ip {
			t.Errorf("%s：可复制 IP = %q, want %q", step, v.overlayIP, w.ip)
		}
	}

	// 1. 冷启动，本机服务还没起来：按钮置灰、图标异常、不许发任何写请求。
	pr.set(down)
	a.bootSequence()
	check("冷启动服务离线", want{StateSvcDown, "连接隧道", false, iconFault, ""})
	if n := len(stub.reqs); n != 0 {
		t.Fatalf("服务离线时冷启动打了 %d 个请求", n)
	}

	// 2. 服务回来、节点停着：正常未连接（淡图标），按钮可点。
	//    IP 照样可复制 —— 它是配置面的值，不是「连通了」的证据。
	pr.set(stopped)
	a.refreshOnce()
	check("服务回来节点停着", want{StateStopped, "连接隧道", true, iconIdle, ip})

	// 3. 用户点一下：进入过渡态，按钮用进行时文案并禁用（进度期间不再发写操作）。
	//    过渡期间没有新探针，可复制 IP 保持上一次的事实，不凭空清空。
	a.onUserToggle()
	waitWorkerLanded(t, a)
	check("点连接后", want{StateStarting, "正在连接…", false, iconIdle, ip})
	if n := len(stub.hits(connectPath)); n != 1 {
		t.Fatalf("connect 打了 %d 次, want 1", n)
	}

	// 4. 探针报在跑：三个面一起翻到「已连接」。
	pr.set(running)
	a.refreshOnce()
	check("连上了", want{StateRunning, "断开隧道", true, iconConnected, ip})

	// 5. 用户复制虚拟 IP：写进剪贴板的就是探针给的那个地址。
	var copied []string
	a.clipFn = func(s string) error { copied = append(copied, s); return nil }
	a.copyOverlayIP()
	if len(copied) != 1 || copied[0] != ip {
		t.Fatalf("剪贴板收到 %v, want [%s]", copied, ip)
	}

	// 6. 用户点断开：同一个按钮，这次走 command 面。
	a.onUserToggle()
	waitWorkerLanded(t, a)
	check("点断开后", want{StateStopping, "正在断开…", false, iconIdle, ip})
	if n := len(stub.hits(commandPath)); n != 1 {
		t.Fatalf("command 打了 %d 次, want 1", n)
	}

	// 7. 探针报停了：IP 仍在（配置没动），复制仍可用 —— 用户断了隧道也常要
	//    把这个地址贴给别人。
	pr.set(stopped)
	a.refreshOnce()
	check("断开了", want{StateStopped, "连接隧道", true, iconIdle, ip})
	a.copyOverlayIP()
	if len(copied) != 2 || copied[1] != ip {
		t.Errorf("第二次复制收到 %v, want 两次都是 %s", copied, ip)
	}

	// 8. 再连一次，然后节点失败：异常态下按钮取「连接隧道」（重试优先），
	//    图标切斜杠 —— 不能因为「刚才连上过」就还显示实心。
	a.onUserToggle()
	waitWorkerLanded(t, a)
	pr.set(failed)
	a.refreshOnce()
	check("连接失败", want{StateFailed, "连接隧道", true, iconFault, ip})

	// 9. 用户点重试并恢复：旅程回到第 4 步的同一组事实，三个面必须一致。
	a.onUserToggle()
	waitWorkerLanded(t, a)
	pr.set(running)
	a.refreshOnce()
	check("重试后恢复", want{StateRunning, "断开隧道", true, iconConnected, ip})

	// 10. 本机服务挂了：什么都读不到，就不许再提供「复制上次那个 IP」——
	//     那会是个无法核实的旧值（§8.1「不复制旧 IP」）。
	pr.set(down)
	a.refreshOnce()
	check("本机服务离线", want{StateSvcDown, "连接隧道", false, iconFault, ""})
	a.copyOverlayIP()
	if len(copied) != 2 {
		t.Errorf("IP 已消失却又写了一次剪贴板：%v", copied)
	}

	// 11. 退出托盘：不停隧道、不停 mynetd —— 整条旅程的请求数到此为止。
	a.onExit()
	waitSettled(t, renders)
	if got, want := len(stub.hits(connectPath)), 3; got != want {
		t.Errorf("全程 connect %d 次, want %d", got, want)
	}
	if got, want := len(stub.hits(commandPath)), 1; got != want {
		t.Errorf("全程 command %d 次, want %d（退出不得停隧道）", got, want)
	}
}
