package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本机 API 写面的合同测试。全部用 testdata 里的**真实响应形状**回放，
// 并断言托盘**实际发出**的请求（路径、方法、body、Origin）。
//
// 不测 onUserStart/onUserStop 的 goroutine 编排：那要动菜单项与 intent 落盘，
// 属于并发那一轮的事。这里锁的是「打哪个端点、怎么判成败、错误文案里有什么」。

type recordedReq struct {
	method string
	path   string
	origin string
	body   string
}

// apiStub 按路径回放 fixture 的本机 API 替身。
type apiStub struct {
	mu          sync.Mutex
	srv         *httptest.Server
	connectFile string
	connectCode int
	commandFile string
	commandCode int
	reqs        []recordedReq
}

// stubSpec 回放规格。参数多且同型，用具名字段避免调用点看错顺序。
type stubSpec struct {
	connectFile string
	connectCode int
	commandFile string
	commandCode int
}

func newStub(t *testing.T, spec stubSpec) *apiStub {
	t.Helper()
	s := &apiStub{connectFile: spec.connectFile, connectCode: spec.connectCode,
		commandFile: spec.commandFile, commandCode: spec.commandCode}
	if s.connectCode == 0 {
		s.connectCode = http.StatusOK
	}
	if s.commandCode == 0 {
		s.commandCode = http.StatusOK
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *apiStub) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	s.mu.Lock()
	s.reqs = append(s.reqs, recordedReq{
		method: r.Method, origin: r.Header.Get("Origin"),
		path: r.URL.Path, body: string(body),
	})
	s.mu.Unlock()
	var file string
	var code int
	switch r.URL.Path {
	case connectPath:
		file, code = s.connectFile, s.connectCode
	case commandPath:
		file, code = s.commandFile, s.commandCode
	default:
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		return
	}
	raw, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		http.Error(w, "fixture missing: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(raw)
}

func (s *apiStub) base() string { return strings.TrimPrefix(s.srv.URL, "http://") }

func (s *apiStub) hits(path string) []recordedReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []recordedReq
	for _, r := range s.reqs {
		if r.path == path {
			out = append(out, r)
		}
	}
	return out
}

// startHits / stopHits 按 argv 区分启停：连接与断开同打 commandPath，
// 用总数会把「断开」算成「启动」。
func (s *apiStub) startHits() int { return s.cmdHitsWith(`"start"`) }
func (s *apiStub) stopHits() int  { return s.cmdHitsWith(`"stop"`) }

func (s *apiStub) cmdHitsWith(frag string) int {
	n := 0
	for _, r := range s.hits(commandPath) {
		if strings.Contains(r.body, frag) {
			n++
		}
	}
	return n
}

// testApp 造一个不碰 systray 的 App：渲染出口换成计数器，探针用替身。
func testApp(t *testing.T, base string, pr Prober) (*App, *int) {
	t.Helper()
	isolateUserConfig(t)
	a := NewApp(pr, base)
	a.hc.Timeout = 2 * time.Second
	renders := 0
	a.renderFn = func() { renders++ }
	return a, &renders
}

// ── 连接：端点与请求形状 ───────────────────────────────────────

// TestStartTunnelUsesCommandEndpoint 锁「托盘只启停本机隧道」这条边界。
//
// D19 的教训是旧版发 `apply --active`（只投影、不起隧道进程）⇒「连接」从未
// 连接过。修好之后改成了打 /api/v1/config/connect —— 那是**接入编排**
// （keys ensure → keys sync --connect → config pull → apply → start → routes），
// 每一步都要过云端鉴权。实测 access token 过期后 `config keys sync --connect`
// 直接 HTTP 401，托盘连不上，而同一台机器上 WebUI 的「启动」（service start）
// 照常可用。托盘的分工是启停本机隧道，云端那半属于配置向导。
func TestStartTunnelUsesCommandEndpoint(t *testing.T) {
	stub := newStub(t, stubSpec{commandFile: "command_ok.json"})
	a, _ := testApp(t, stub.base(), &fakeProber{})

	if err := a.startTunnel(); err != nil {
		t.Fatalf("command_ok 应成功: %v", err)
	}
	got := stub.hits(commandPath)
	if len(got) != 1 {
		t.Fatalf("打了 %d 次 %s, want 1", len(got), commandPath)
	}
	if got[0].method != http.MethodPost {
		t.Errorf("method=%s, want POST", got[0].method)
	}
	// 安全红线：embed WebUI 必须校验 Origin/Host
	if got[0].origin != "http://"+stub.base() {
		t.Errorf("Origin=%q", got[0].origin)
	}
	// 启停不碰会走云端的接入编排
	if n := len(stub.hits(connectPath)); n != 0 {
		t.Errorf("不该打 %s: %d 次", connectPath, n)
	}
	if !strings.Contains(got[0].body, `"service"`) || !strings.Contains(got[0].body, `"start"`) {
		t.Errorf("body 应含 service start: %q", got[0].body)
	}
	// 驱动名由 cmdService 从 bundle 解析（gnb/wireguard 两种节点同一套托盘逻辑）
	if strings.Contains(got[0].body, "gnb") || strings.Contains(got[0].body, "wireguard") {
		t.Errorf("托盘不该写死驱动名: %q", got[0].body)
	}
}

// ── 连接：失败面 ───────────────────────────────────────────────

// TestStartTunnelConflict 锁 409 与「不自动重试」。
// 服务端给 retryable=true + Retry-After，但启停是有副作用的写操作，
// 用户没要求的启停不能替他做。
func TestStartTunnelConflict(t *testing.T) {
	stub := newStub(t, stubSpec{commandFile: "command_409.json", commandCode: http.StatusConflict})
	a, _ := testApp(t, stub.base(), &fakeProber{})

	err := a.startTunnel()
	if !errors.Is(err, errInProgress) {
		t.Fatalf("409 应返回 errInProgress, got %v", err)
	}
	if n := len(stub.hits(commandPath)); n != 1 {
		t.Fatalf("客户端自动重试了：%d 次请求", n)
	}
	if strings.Contains(err.Error(), "operation_in_progress") {
		t.Errorf("内部 code 不该进文案: %q", err.Error())
	}
}

// TestStartTunnelFailureDoesNotLeakStderr 锁 D7。
//
// command 面失败时 UI 只拿 message，绝不碰 stderr —— fixture 里那条 stderr
// 刻意带着绝对路径和「密钥指纹 <64 hex>」（本地实测原样透出过一次），
// 它们一条都不该出现在菜单文案里。
func TestStartTunnelFailureDoesNotLeakStderr(t *testing.T) {
	stub := newStub(t, stubSpec{commandFile: "command_500.json", commandCode: http.StatusInternalServerError})
	a, _ := testApp(t, stub.base(), &fakeProber{})

	err := a.startTunnel()
	if err == nil {
		t.Fatal("500 必须报错")
	}
	msg := err.Error()
	const fingerprint = "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeeeffffffff0000000011111111"
	if strings.Contains(msg, fingerprint) {
		t.Errorf("密钥指纹泄漏进 UI 文案: %q", msg)
	}
	if strings.Contains(msg, "address.conf") || strings.Contains(msg, "/opt/mynet/conf") {
		t.Errorf("stderr 里的路径细节泄漏: %q", msg)
	}
	if strings.ContainsAny(msg, "\n\r\t\x1b\x07") {
		t.Errorf("控制字符/换行泄漏: %q", msg)
	}
	if n := len([]rune(msg)); n > MaxMenuRunes*2 {
		t.Errorf("文案没截断: %d runes", n)
	}
}

// TestStartTunnelSvcDown 打不到 API：文案必须是「服务不可用」，
// 且不带底层 dial 错误里的地址（那会把内部端口/URL 印到界面上）。
func TestStartTunnelSvcDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := strings.TrimPrefix(srv.URL, "http://")
	srv.Close() // 端口立刻无人监听

	a, _ := testApp(t, base, &fakeProber{})
	err := a.startTunnel()
	if err == nil {
		t.Fatal("连不上必须报错")
	}
	if err.Error() != msgSvcDown {
		t.Fatalf("got %q, want %q", err.Error(), msgSvcDown)
	}
	if strings.Contains(err.Error(), base) || strings.Contains(err.Error(), "dial") {
		t.Errorf("底层错误细节泄漏: %q", err.Error())
	}
}

// TestUnparsableResponseIsNotSuccess 200 + 非 JSON：判失败，不崩，
// 也不当成「未连接」或「成功」。
func TestUnparsableResponseIsNotSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>gateway</html>"))
	}))
	t.Cleanup(srv.Close)

	a, _ := testApp(t, strings.TrimPrefix(srv.URL, "http://"), &fakeProber{})
	if err := a.startTunnel(); err == nil || err.Error() != msgUnparsable {
		t.Fatalf("startTunnel got %v, want %q", err, msgUnparsable)
	}
	if err := a.stopTunnel(); err == nil || err.Error() != msgUnparsable {
		t.Fatalf("stopTunnel got %v, want %q", err, msgUnparsable)
	}
}

// ── 断开：命令形状与结果判定 ───────────────────────────────────

// TestStopTunnelOmitsDriver 锁 D14。
// 驱动参数是可选的，CLI 会从 bundle 自己解析（cmd_apply.go:173-189）。
// 旧版写死 "gnb"：wireguard 节点上停的是没在跑的 gnb，wg 隧道原样留着，
// 而托盘会显示「已断开」。
func TestStopTunnelOmitsDriver(t *testing.T) {
	stub := newStub(t, stubSpec{commandFile: "command_ok.json"})
	a, _ := testApp(t, stub.base(), &fakeProber{})

	if err := a.stopTunnel(); err != nil {
		t.Fatalf("command_ok 应成功: %v", err)
	}
	got := stub.hits(commandPath)
	if len(got) != 1 {
		t.Fatalf("打了 %d 次 command, want 1", len(got))
	}
	var payload struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal([]byte(got[0].body), &payload); err != nil {
		t.Fatalf("body 不是 JSON: %v (%q)", err, got[0].body)
	}
	if len(payload.Argv) != 2 || payload.Argv[0] != "service" || payload.Argv[1] != "stop" {
		t.Fatalf("argv=%v, want [service stop]（不带驱动名）", payload.Argv)
	}
	if got[0].origin == "" {
		t.Error("没带 Origin")
	}
}

// TestCommandBusinessFailureAt200 锁 D5：HTTP 200 + success:false 是业务失败。
// 旧版只判 StatusCode>=300，于是「当前节点未获准使用出口代理」这类拒绝被
// 当成成功，托盘报「已断开/已连接」。
func TestCommandBusinessFailureAt200(t *testing.T) {
	stub := newStub(t, stubSpec{commandFile: "command_200_business_failure.json", commandCode: http.StatusOK})
	a, _ := testApp(t, stub.base(), &fakeProber{})

	err := a.stopTunnel()
	if err == nil {
		t.Fatal("200+success:false 被当成成功了（D5 回归失败）")
	}
	if !strings.Contains(err.Error(), "未获准使用出口代理") {
		t.Fatalf("原因丢了: %q", err.Error())
	}
	if strings.Contains(err.Error(), "proxy_exit_denied") {
		t.Errorf("内部 code 不该进文案: %q", err.Error())
	}
}

// TestCommandErrorHidesRawBody 锁 D7：stderr / 绝对路径 / 密钥指纹不进 UI。
func TestCommandErrorHidesRawBody(t *testing.T) {
	stub := newStub(t, stubSpec{commandFile: "command_500.json", commandCode: http.StatusInternalServerError})
	a, _ := testApp(t, stub.base(), &fakeProber{})

	err := a.stopTunnel()
	if err == nil {
		t.Fatal("500 必须报错")
	}
	msg := err.Error()
	for _, banned := range []string{
		"address.conf 缺失",
		"/opt/mynet/conf/local/",
		"aaaaaaaabbbbbbbbcccccccc",
		"apply: gnb 启动失败",
	} {
		if strings.Contains(msg, banned) {
			t.Errorf("stderr 原文泄漏进文案 %q: 命中 %q", msg, banned)
		}
	}
}

func TestCommandConflictNoRetry(t *testing.T) {
	stub := newStub(t, stubSpec{commandFile: "command_409.json", commandCode: http.StatusConflict})
	a, _ := testApp(t, stub.base(), &fakeProber{})

	if err := a.stopTunnel(); !errors.Is(err, errInProgress) {
		t.Fatalf("got %v, want errInProgress", err)
	}
	if n := len(stub.hits(commandPath)); n != 1 {
		t.Fatalf("自动重试了：%d 次请求", n)
	}
}

// 导入功能的回归测试随功能一起删除（D13 改判：配置备份走 WebUI，托盘不再
// 提供导入/导出菜单）。其锁住的 D12 原则 ——「导入与连接是两次明确操作，
// 导入后绝不自发起隧道」—— 转移给将来的 daemon 导入端点，由那一侧的用例守。

// ── 渲染出口的文本卫生 ─────────────────────────────────────────

// TestStatusTextSanitizes 锁 D8/D20 在渲染出口这一层：
// one_line 与错误原因都必须清洗后才上屏，标题不得被长原因撑爆。
func TestStatusTextSanitizes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "status_secret_injection.json"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	var env struct {
		Data struct {
			OneLine string `json:"one_line"`
			Detail  string `json:"detail"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(env.Data.OneLine, "FAKE-SECRET-PAT-") {
		t.Fatalf("fixture 变了，注入样本没了: %q", env.Data.OneLine)
	}

	a, _ := testApp(t, "127.0.0.1:0", &fakeProber{})
	a.lastOneLine = env.Data.OneLine
	a.fsm.state = StateDegraded

	title, tooltip := a.statusText()
	if title != "状态："+StateDegraded.Label() {
		t.Errorf("标题被污染: %q", title)
	}
	if n := len([]rune(title)); n > MaxMenuRunes {
		t.Errorf("标题超菜单上限 %d: %d runes", MaxMenuRunes, n)
	}
	for _, banned := range []string{"FAKE-SECRET-PAT-", "\x1b", "\n", "\a", "bbbbbbbbcccccccc"} {
		if strings.Contains(tooltip, banned) {
			t.Errorf("tooltip 泄漏 %q: %q", banned, tooltip)
		}
	}
	if !strings.Contains(tooltip, StateDegraded.Label()) {
		t.Errorf("tooltip 丢了状态短语: %q", tooltip)
	}
	// detail 里的 64 位假私钥也不该出现（它经 lastErr 走同一条出口）
	a.fsm.state = StateFailed
	a.fsm.lastErr = env.Data.Detail
	_, tooltip = a.statusText()
	if strings.Contains(tooltip, "bbbbbbbbcccccccc") {
		t.Errorf("lastErr 里的假私钥泄漏: %q", tooltip)
	}
}

// TestStatusTextSvcDownVsStopped P3 验收「服务离线/节点停止文案不同」的
// 渲染层落点：两种情况在界面上必须看得出区别。
func TestStatusTextSvcDownVsStopped(t *testing.T) {
	a, _ := testApp(t, "127.0.0.1:0", &fakeProber{})

	a.fsm.On(EvSvcDown)
	_, down := a.statusText()
	a.fsm.On(EvDaemonDown)
	_, stopped := a.statusText()

	if down == stopped {
		t.Fatalf("服务不可用与未连接的文案相同: %q", down)
	}
	if !strings.Contains(down, "本机服务") {
		t.Errorf("服务离线文案没提服务: %q", down)
	}
	if strings.Contains(stopped, msgSvcDown) {
		t.Errorf("未连接不该顶着服务离线的文案: %q", stopped)
	}
}

// ── 面板地址 ───────────────────────────────────────────────────

// TestPanelURLPointsAtUI 锁住「根路径是 JSON 壳」这个坑。
//
// 旧版 PanelURL 返回 http://base/（JSON 壳）且**无人调用**，openPanel 自己
// 另拼一份 /ui/ —— 两份地址必然漂移，漂了也只是「点开看到一坨 JSON」，
// 不报错、没人发现。现在 openPanel 走 PanelURL，这里锁住它的落点。
func TestPanelURLPointsAtUI(t *testing.T) {
	const base = "127.0.0.1:8096"
	if got, want := PanelURL(base), "http://"+base+"/ui/"; got != want {
		t.Errorf("PanelURL(%q) = %q, want %q", base, got, want)
	}
}

// ── 冷启动编排 ─────────────────────────────────────────────────

// bootProbes 冷启动测试用的三种服务/节点事实。
var bootProbes = map[string]Probe{
	"stopped": {Up: true, Valid: true, NodeState: "STOPPED", OneLine: "已停止"},
	"running": {Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: true, OneLine: "运行中"},
	"down":    {Up: false},
}

func newBootApp(t *testing.T, pr Probe) (*App, *apiStub) {
	t.Helper()
	stub := newStub(t, stubSpec{connectFile: "connect_ok.json", commandFile: "command_ok.json"})
	a, _ := testApp(t, stub.base(), &fakeProber{script: []Probe{pr}})
	return a, stub
}

// TestBootSequenceSyncsAutoMenu 锁 D10/D11 在冷启动这一侧：
// 菜单初值是「状态未知」，bootSequence 必须把它换成真实状态，
// 否则用户从登录起就一直看着一个占位文案。
func TestBootSequenceSyncsAutoMenu(t *testing.T) {
	isolateUserConfig(t)
	a, _ := newBootApp(t, bootProbes["stopped"])
	a.autoStart = &fakeAutoStarter{on: true}

	a.bootSequence()

	if got := a.snapshot().autoTitle; got != "登录时启动 ✓" {
		t.Errorf("冷启动后 autoTitle = %q, want %q", got, "登录时启动 ✓")
	}
}

// TestBootSequenceRestoresIntent 锁「托盘启动 → 先拉 mynetd → 读 last_intent →
// 要跑则再起节点」这条用户约定，**并且**只在真能起的时候才发。
//
// 旧版无条件发：节点已在跑时既多发一次有副作用的写操作，又不进 Starting，
// 用户在菜单上看不到任何进度。
func TestBootSequenceRestoresIntent(t *testing.T) {
	cases := []struct {
		name      string
		pr        Probe
		wantNode  bool
		wantFire  bool
		wantState State
	}{
		{"要跑且停着则起", bootProbes["stopped"], true, true, StateStarting},
		{"要跑但已在跑则不重复发", bootProbes["running"], true, false, StateRunning},
		{"要跑但服务离线则不发", bootProbes["down"], true, false, StateSvcDown},
		{"不要跑则不发", bootProbes["stopped"], false, false, StateStopped},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isolateUserConfig(t)
			a, stub := newBootApp(t, c.pr)
			a.autoStart = &fakeAutoStarter{}
			// 意图文件必须在 testApp 之后写：testApp 会换掉 HOME。
			if err := saveIntent(trayIntent{WantNode: c.wantNode}); err != nil {
				t.Fatalf("布置意图: %v", err)
			}

			a.bootSequence()

			if c.wantFire {
				waitWorkerLanded(t, a)
			}
			if n := stub.startHits(); (n > 0) != c.wantFire {
				t.Errorf("启动请求打了 %d 次, wantFire=%v", n, c.wantFire)
			}
			if got := a.fsm.State(); got != c.wantState {
				t.Errorf("状态 = %s, want %s", got, c.wantState)
			}
		})
	}
}

func (s *apiStub) reqCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.reqs) }
