package mynet

// 回归（2026-11-18 健壮性收口）：云端域名清单拉取失败/超时不得杀 dns-hook worker、
// 不得停 DNS 监听——沿用已落盘清单继续服务，后台退避重试；后续拉取成功热更新。
// 背景：拉取超时与「worker 被杀（server exit: use of closed network connection）」
// 曾同时出现在网络抖动窗口，DNS/加速全哑。

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/shared/config"
)

// 模拟拉取失败 → 服务仍存活、旧清单仍生效、后续拉取成功后热更新。
func TestDNSHookSurvivesDomainListFetchFailureThenHotUpdates(t *testing.T) {
	t.Setenv("MYNET_CONFIG_HOME", "")
	h := newSessionHarness(t) // MYNET_HOME/tmp + 路由假件（不碰系统路由表）
	// 旧清单已在盘上（此前成功拉取过）——拉取失败期间必须继续生效。
	if err := config.SaveProxyDomainBaseline(ProxyStateDir(), &config.ProxyDomainBaseline{
		Proxied: []string{"old.accel.test"}, Version: "v-old",
	}); err != nil {
		t.Fatal(err)
	}
	if err := SetProxyEnabled(true); err != nil {
		t.Fatal(err)
	}

	var fail atomic.Bool
	fail.Store(true)
	var proxyFetches atomic.Int64
	ctl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "proxy-domains.txt") {
			proxyFetches.Add(1)
		}
		if fail.Load() {
			http.Error(w, "offline", 503)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "proxy-domains.txt"):
			_, _ = w.Write([]byte("new.accel.test\n"))
		case strings.HasSuffix(r.URL.Path, "direct-domains.txt"):
			http.NotFound(w, r) // 云端未发布直连表不算失败
		case strings.HasSuffix(r.URL.Path, "proxy-ips.txt"):
			http.NotFound(w, r) // 云端未发布预设 IP 表不算失败
		case strings.HasSuffix(r.URL.Path, "ad-domains.txt"):
			_, _ = w.Write([]byte("ad.test\n"))
		case strings.HasSuffix(r.URL.Path, "malware-domains.txt"):
			_, _ = w.Write([]byte("malware.test\n"))
		case strings.HasSuffix(r.URL.Path, "dns-clean.txt"):
			_, _ = w.Write([]byte("1.1.1.1\n"))
		default:
			t.Errorf("unexpected list dependency: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer ctl.Close()
	t.Setenv("MYNET_APPS_INDEX", ctl.URL)

	server, err := newConfiguredHookServer()
	if err != nil {
		t.Fatal(err)
	}
	server.pins = h.s
	server.forward = func(_ string, q []byte, _ time.Duration) ([]byte, error) {
		name, _ := parseDNSQuestion(q)
		ip := map[string]string{"old.accel.test": "192.0.2.1", "new.accel.test": "192.0.2.2"}[name]
		if ip == "" {
			ip = "192.0.2.9"
		}
		return buildTestDNSAnswer(q, ip), nil
	}
	server.tun = func() string { return "test0" }
	server.baselineSteady = 20 * time.Millisecond
	server.baselineRetryBase = 5 * time.Millisecond
	server.baselineRetryCap = 10 * time.Millisecond

	// 找一个空闲 UDP 端口（避开 :53，不需要系统 DNS 监控协程）
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.LocalAddr().String()
	probe.Close()

	done := make(chan struct{})
	served := make(chan error, 1)
	go func() { served <- serveDNSHook(server, addr, done) }()
	defer func() {
		close(done)
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("serveDNSHook: %v", err) // 只有 done 关闭才允许返回 nil
			}
		case <-time.After(3 * time.Second):
			t.Error("serveDNSHook 未随 done 退出")
		}
	}()

	query := func(name string) {
		t.Helper()
		conn, err := net.Dial("udp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write(buildTestDNSQuery(name)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 512)
		n, err := conn.Read(buf)
		if err != nil || n < 12 || buf[2]&0x80 == 0 {
			t.Fatalf("查询 %s 无有效应答: n=%d err=%v", name, n, err)
		}
	}
	// 等 serveDNSHook 的 UDP 监听就绪（goroutine 启动竞态）
	ready := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.Dial("udp", addr)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
			_, _ = conn.Write(buildTestDNSQuery("ready.probe"))
			buf := make([]byte, 512)
			n, rerr := conn.Read(buf)
			conn.Close()
			if rerr == nil && n >= 12 {
				break
			}
		}
		if time.Now().After(ready) {
			t.Fatal("serveDNSHook 监听未就绪")
		}
		time.Sleep(10 * time.Millisecond)
	}
	pinned := func(name string) bool {
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		_, ok := h.s.Domains[name]
		return ok
	}

	// ① 服务存活 + 旧清单生效：拉取失败中仍能应答并钉扎旧域名
	query("old.accel.test")
	if !pinned("old.accel.test") {
		t.Fatal("旧清单未生效（old.accel.test 未钉扎）")
	}

	// ② 拉取持续失败：后台退避重试在跑（≥2 次尝试），期间监听不倒、旧清单仍在
	deadline := time.Now().Add(2 * time.Second)
	for proxyFetches.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if proxyFetches.Load() < 2 {
		t.Fatalf("拉取失败后未退避重试（attempts=%d）", proxyFetches.Load())
	}
	query("old.accel.test")
	if !pinned("old.accel.test") {
		t.Fatal("拉取失败期间旧清单失效")
	}

	// ③ 拉取恢复 → 热更新：新清单进入运行中规则，旧清单退出
	fail.Store(false)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		query("new.accel.test")
		if pinned("new.accel.test") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !pinned("new.accel.test") {
		t.Fatal("拉取成功后未热更新（new.accel.test 未钉扎）")
	}
	query("old.accel.test")
	if pinned("old.accel.test") {
		t.Fatal("热更新后旧清单应退出（old.accel.test 不应再钉扎）")
	}
}

// 循环合同：失败只降级不退出；缓存清单不受影响；恢复后热更新信号 + 新清单落盘。
func TestRunBaselineRefreshLoopDegradesToCachedAndRecovers(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	t.Setenv("MYNET_CONFIG_HOME", "")
	if err := config.SaveProxyDomainBaseline(ProxyStateDir(), &config.ProxyDomainBaseline{
		Proxied: []string{"old.test"}, Version: "v1",
	}); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int64
	var fail atomic.Bool
	fail.Store(true)
	refresh := func(context.Context) error {
		attempts.Add(1)
		if fail.Load() {
			return errors.New("ctl unreachable")
		}
		return config.SaveProxyDomainBaseline(ProxyStateDir(), &config.ProxyDomainBaseline{
			Proxied: []string{"new.test"}, Version: "v2",
		})
	}
	updated := make(chan struct{}, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runBaselineRefreshLoop(ctx, refresh, func() {
		select {
		case updated <- struct{}{}:
		default:
		}
	}, 20*time.Millisecond, 5*time.Millisecond, 10*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for attempts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if attempts.Load() < 2 {
		t.Fatal("拉取失败后未退避重试")
	}
	base, err := config.LoadProxyDomainBaseline(ProxyStateDir())
	if err != nil || base == nil || len(base.Proxied) != 1 || base.Proxied[0] != "old.test" {
		t.Fatalf("拉取失败期间缓存清单被改动: %+v err=%v", base, err)
	}
	fail.Store(false)
	select {
	case <-updated:
	case <-time.After(2 * time.Second):
		t.Fatal("拉取恢复后未触发热更新")
	}
	base, _ = config.LoadProxyDomainBaseline(ProxyStateDir())
	if base == nil || len(base.Proxied) != 1 || base.Proxied[0] != "new.test" {
		t.Fatalf("拉取成功后未落新清单: %+v", base)
	}
}

// 失败退避：30s 起倍增、15min 封顶。
func TestBaselineRetryDelayBackoff(t *testing.T) {
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{
		{0, 30 * time.Second},
		{1, 30 * time.Second},
		{2, 60 * time.Second},
		{3, 120 * time.Second},
		{5, 480 * time.Second},
		{8, 15 * time.Minute},
		{99, 15 * time.Minute},
	} {
		if got := baselineRetryDelay(tc.failures, 30*time.Second, 15*time.Minute); got != tc.want {
			t.Errorf("failures=%d: %v, want %v", tc.failures, got, tc.want)
		}
	}
}
