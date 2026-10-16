package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 探针测试全部走 testdata 里的**真实响应形状**，不手搓结构体：
// node-status 的信封、字段名、缺失情况都取自 mynetd 的实际输出
// （localapi/node_status_handler.go:37 / :22 / :30-34）。
//
// fixture 里的节点号、overlay 地址、密钥串都是虚构的。

// serveFixture 起一个只在本次测试内存活的 node-status 服务。
// code<=0 时用 200。返回其 host:port。
func serveFixture(t *testing.T, file string, code int) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatalf("读 fixture %s: %v", file, err)
	}
	if code <= 0 {
		code = http.StatusOK
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 顺带锁住托盘确实打的是只读探针端点，且带 Origin（安全红线）
		if r.URL.Path != "/api/v1/node-status" {
			t.Errorf("探针打了 %s，want /api/v1/node-status", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("探针用了 %s，want GET", r.Method)
		}
		if r.Header.Get("Origin") == "" {
			t.Error("探针没带 Origin")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func probeFixture(t *testing.T, file string, code int) Probe {
	t.Helper()
	base := serveFixture(t, file, code)
	p := NewHTTPProber(base)
	p.HC.Timeout = 2 * time.Second
	pr, err := p.Probe()
	if err != nil {
		t.Fatalf("%s: Probe 返回 err=%v（连不上才是常态，不该报错）", file, err)
	}
	return pr
}

// TestProbeFixtures 每个 fixture 的解析结果与它应产生的事件。
func TestProbeFixtures(t *testing.T) {
	cases := []struct {
		file    string
		code    int
		up      bool
		valid   bool
		state   string
		procUp  bool
		wantEv  Event
		wantFSM State
	}{
		// 服务在、事实可信
		{"status_running.json", 0, true, true, "RUNNING", true, EvNodeHealthy, StateRunning},
		{"status_stopped.json", 0, true, true, "STOPPED", false, EvDaemonDown, StateStopped},
		{"status_degraded_routes.json", 0, true, true, "DEGRADED", true, EvNodeDegraded, StateDegraded},
		{"status_mismatch.json", 0, true, true, "DEGRADED", true, EvNodeDegraded, StateDegraded},
		// 节点未安装/未配置/已退出：都是「未连接」，不是异常（D3）
		{"status_uninstalled.json", 0, true, true, "UNINSTALLED", false, EvDaemonDown, StateStopped},
		{"status_installed_unconfigured.json", 0, true, true, "INSTALLED", false, EvDaemonDown, StateStopped},
		// FAILED 但进程不在 = 没起来，显示「未连接」而不是「连接异常」
		{"status_failed_pid.json", 0, true, true, "FAILED", false, EvDaemonDown, StateStopped},
		// 服务在说话但说的听不懂（D4/D17）：不得当成「未连接」
		{"status_invalid_200.json", 0, true, false, "", false, EvSvcInvalid, StateSvcDown},
		{"status_executor_500.json", http.StatusInternalServerError, true, false, "", false, EvSvcInvalid, StateSvcDown},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			pr := probeFixture(t, c.file, c.code)
			if pr.Up != c.up || pr.Valid != c.valid {
				t.Fatalf("Up=%v Valid=%v, want Up=%v Valid=%v", pr.Up, pr.Valid, c.up, c.valid)
			}
			if pr.NodeState != c.state || pr.ProcessUp != c.procUp {
				t.Fatalf("state=%q process_up=%v, want %q/%v", pr.NodeState, pr.ProcessUp, c.state, c.procUp)
			}
			ev := eventFromProbe(pr)
			if ev != c.wantEv {
				t.Fatalf("eventFromProbe = %s, want %s", ev, c.wantEv)
			}
			// 事件真的能把 FSM 送到该去的地方
			f := NewFSM()
			if st, _, _ := f.On(ev); st != c.wantFSM {
				t.Fatalf("冷启动 + %s = %s, want %s", ev, st, c.wantFSM)
			}
		})
	}
}

// TestProbeSvcDown API 完全无响应 —— 常态，不是错误。
func TestProbeSvcDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := strings.TrimPrefix(srv.URL, "http://")
	srv.Close() // 立刻关掉，端口无人监听

	p := NewHTTPProber(base)
	p.HC.Timeout = 500 * time.Millisecond
	pr, err := p.Probe()
	if err != nil {
		t.Fatalf("连不上不该报 err: %v", err)
	}
	if pr.Up {
		t.Fatal("Up 应为 false")
	}
	if ev := eventFromProbe(pr); ev != EvSvcDown {
		t.Fatalf("got %s, want SvcDown", ev)
	}
	if got := pr.String(); got != "svc-down" {
		t.Fatalf("String()=%q", got)
	}
}

// TestProbeNon200IsInvalid 403（-api 指到非回环）/404（路由没了）都算
// 「服务在、状态拿不到」，不能塌成「未连接」。
func TestProbeNon200IsInvalid(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusBadGateway} {
		base := serveFixture(t, "status_running.json", code)
		pr, err := NewHTTPProber(base).Probe()
		if err != nil {
			t.Fatalf("%d: %v", code, err)
		}
		if !pr.Up || pr.Valid {
			t.Fatalf("%d: Up=%v Valid=%v, want true/false", code, pr.Up, pr.Valid)
		}
		if pr.NodeState != "" {
			t.Fatalf("%d: 非200 不该解析出 state=%q", code, pr.NodeState)
		}
		if ev := eventFromProbe(pr); ev != EvSvcInvalid {
			t.Fatalf("%d: got %s, want SvcInvalid", code, ev)
		}
	}
}

// TestProbeGarbageBody 200 + 非 JSON：判不可信，不崩、不当未连接。
func TestProbeGarbageBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	t.Cleanup(srv.Close)
	pr, err := NewHTTPProber(strings.TrimPrefix(srv.URL, "http://")).Probe()
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !pr.Up || pr.Valid {
		t.Fatalf("Up=%v Valid=%v, want true/false", pr.Up, pr.Valid)
	}
	if ev := eventFromProbe(pr); ev != EvSvcInvalid {
		t.Fatalf("got %s, want SvcInvalid", ev)
	}
}

// TestProbeKeepsRawOneLine D20 的前提：探针**不**清洗，原样带出，
// 清洗责任在渲染出口（text.go / render）。这里锁住这条分工，
// 免得日后有人在探针里洗一半、在渲染里又洗一半。
func TestProbeKeepsRawOneLine(t *testing.T) {
	pr := probeFixture(t, "status_secret_injection.json", 0)
	if !pr.Valid {
		t.Fatal("应解析成功")
	}
	if !strings.Contains(pr.OneLine, "\x1b[31m") {
		t.Fatalf("期望原样保留控制字符（由 render 清洗），got %q", pr.OneLine)
	}
	if !strings.Contains(pr.OneLine, "FAKE-SECRET-PAT-") {
		t.Fatalf("期望原样保留疑似令牌，got %q", pr.OneLine)
	}
	// 清洗后必须两样都没了 —— 这就是 P3 要接的出口
	clean := safeText(pr.OneLine, MaxMenuRunes*2)
	if strings.Contains(clean, "FAKE-SECRET-PAT-") {
		t.Fatalf("safeText 没遮住令牌: %q", clean)
	}
	if strings.Contains(clean, "\x1b") || strings.Contains(clean, "\n") || strings.Contains(clean, "\a") {
		t.Fatalf("safeText 没去掉控制字符: %q", clean)
	}
	if n := len([]rune(clean)); n > MaxMenuRunes*2 {
		t.Fatalf("safeText 没截断: %d runes", n)
	}
}

// TestEventFromProbe 映射表（合成输入，补齐 fixture 覆盖不到的角落）。
func TestEventFromProbe(t *testing.T) {
	cases := []struct {
		p    Probe
		want Event
	}{
		{Probe{Up: false}, EvSvcDown},
		{Probe{Up: true, Valid: false}, EvSvcInvalid},
		// Valid=false 时即使带了 state 也不可信
		{Probe{Up: true, Valid: false, NodeState: "RUNNING", ProcessUp: true}, EvSvcInvalid},
		{Probe{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: true}, EvNodeHealthy},
		{Probe{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: false}, EvDaemonDown},
		{Probe{Up: true, Valid: true, NodeState: "DEGRADED", ProcessUp: true}, EvNodeDegraded},
		{Probe{Up: true, Valid: true, NodeState: "DEGRADED", ProcessUp: false}, EvDaemonDown},
		{Probe{Up: true, Valid: true, NodeState: "FAILED", ProcessUp: true}, EvNodeFailed},
		{Probe{Up: true, Valid: true, NodeState: "FAILED", ProcessUp: false}, EvDaemonDown},
		{Probe{Up: true, Valid: true, NodeState: "STOPPED"}, EvDaemonDown},
		{Probe{Up: true, Valid: true, NodeState: "UNINSTALLED"}, EvDaemonDown},
		{Probe{Up: true, Valid: true, NodeState: "INSTALLED"}, EvDaemonDown},
		{Probe{Up: true, Valid: true, NodeState: "CONFIGURED"}, EvDaemonDown},
		{Probe{Up: true, Valid: true, NodeState: ""}, EvDaemonDown},
		// 未知 state 值：保守判未连接，不猜
		{Probe{Up: true, Valid: true, NodeState: "SOMETHING_NEW", ProcessUp: true}, EvDaemonDown},
	}
	for _, c := range cases {
		if got := eventFromProbe(c.p); got != c.want {
			t.Errorf("eventFromProbe(%+v) = %s, want %s", c.p, got, c.want)
		}
	}
}

func TestProbeString(t *testing.T) {
	if got := (Probe{Up: true, Valid: false}).String(); got != "svc-invalid" {
		t.Errorf("got %q", got)
	}
	got := (Probe{Up: true, Valid: true, NodeState: "RUNNING", ProcessUp: true}).String()
	if !strings.Contains(got, "RUNNING") {
		t.Errorf("got %q", got)
	}
}

// fakeProber 测试替身：按脚本依次返回，用于驱动 App（P1/P2 用）。
type fakeProber struct {
	script []Probe
	i      int
	err    error
	calls  int
}

func (f *fakeProber) Probe() (Probe, error) {
	f.calls++
	if f.err != nil {
		return Probe{}, f.err
	}
	if len(f.script) == 0 {
		return Probe{Up: false}, nil
	}
	if f.i >= len(f.script) {
		return f.script[len(f.script)-1], nil
	}
	p := f.script[f.i]
	f.i++
	return p, nil
}

// TestProbeFeedsFSM 真实探针 → eventFromProbe → FSM 的完整链路，
// 走一遍用户会经历的剧本：在跑 → 受限 → 恢复 → 停了 → 执行器崩了 → 回来。
func TestProbeFeedsFSM(t *testing.T) {
	steps := []struct {
		file    string
		code    int
		want    State
		comment string
	}{
		{"status_running.json", 0, StateRunning, "冷启动即在跑"},
		{"status_degraded_routes.json", 0, StateDegraded, "路由掉了"},
		{"status_running.json", 0, StateRunning, "自己恢复"},
		{"status_stopped.json", 0, StateStopped, "进程停了"},
		{"status_executor_500.json", http.StatusInternalServerError, StateSvcDown, "执行器崩了"},
		{"status_stopped.json", 0, StateStopped, "执行器回来了"},
	}
	f := NewFSM()
	for i, s := range steps {
		base := serveFixture(t, s.file, s.code)
		pr, err := NewHTTPProber(base).Probe()
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		ev := eventFromProbe(pr)
		prev := f.State()
		if st, _, _ := f.On(ev); st != s.want {
			t.Fatalf("step %d (%s) %s: %s + %s = %s, want %s",
				i, s.comment, s.file, prev, ev, st, s.want)
		}
	}
}

// TestProbeFeedsFSM_StartThenHealth D1 的端到端断言：点了连接以后，
// 探针报出的健康事实必须能结束 Starting —— 旧版只能等 20s 超时。
func TestProbeFeedsFSM_StartThenHealth(t *testing.T) {
	f := NewFSM()

	stopped := serveFixture(t, "status_stopped.json", 0)
	pr, _ := NewHTTPProber(stopped).Probe()
	f.On(eventFromProbe(pr))
	if f.State() != StateStopped {
		t.Fatalf("起始 %s", f.State())
	}

	if _, act, _ := f.On(EvStart); act != ActionDoStart {
		t.Fatal("应发出 DoStart")
	}
	if f.State() != StateStarting {
		t.Fatalf("点连接后 %s", f.State())
	}

	// 探针还是 stopped：过渡态不该被打断
	pr, _ = NewHTTPProber(stopped).Probe()
	f.On(eventFromProbe(pr))
	if f.State() != StateStarting {
		t.Fatalf("连接中探针回声不该打断: %s", f.State())
	}

	// 探针报健康：立刻结束过渡
	running := serveFixture(t, "status_running.json", 0)
	pr, _ = NewHTTPProber(running).Probe()
	st, _, _ := f.On(eventFromProbe(pr))
	if st != StateRunning {
		t.Fatalf("D1 回归失败：健康事实没能结束 Starting，got %s", st)
	}
	if !f.CanStop() {
		t.Fatal("Running 应能断开")
	}
}
