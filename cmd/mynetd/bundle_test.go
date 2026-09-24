package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

// ── 夹具 ─────────────────────────────────────────────────────

// validEnvelope 用**真的**编码器造一份 envelope。
// 不手写 JSON 字符串：那样测的是「我写的字符串能被解析」，而不是「两端形状一致」。
func validEnvelope(t *testing.T, generation string) []byte {
	t.Helper()
	env := &domain.BundleEnvelope{
		SchemaVersion: 2,
		Generation:    generation,
		// schema 把 checksum 列为 required，所以夹具必须带上。
		// 这里填字面量而不真算：Save 刻意**不验校验和的值**（见 validateEnvelope 注释），
		// 本测试要验的是「required 字段齐不齐」，不是 canonicalization。
		Checksum:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		GeneratedAt: "2026-09-25T00:00:00Z",
		Node:        domain.NodeRef{ID: "1245816621804034", Interface: "gnb0", MTU: 1400},
		Driver:      domain.VPNDriverType("gnb"),
		Network:     domain.NetworkIntent{Addresses: []string{"10.23.0.5/24"}},
	}
	raw, err := domain.EncodeBundleEnvelope(env)
	if err != nil {
		t.Fatalf("EncodeBundleEnvelope: %v", err)
	}
	return raw
}

// tempStore 把 MYNET_HOME 指到临时目录，于是 shared.VarDir() 落在里面 ——
// 与 mynet/cmd_refresh.go 的 varDir() 同一套口径，不会碰到真实的 ~/.mynet。
func tempStore(t *testing.T) *bundleStore {
	t.Helper()
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	return newBundleStore()
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s: %v", p, err)
	}
	return raw
}

// ── Save：校验后落盘 ─────────────────────────────────────────

func TestBundleSaveWritesDecodableFile(t *testing.T) {
	s := tempStore(t)
	body := validEnvelope(t, "gen-1")

	if err := s.Save(body, `"etag-1"`); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 落盘的内容必须能被**执行器用的那个解码器**解出来 —— 这才是「两端一致」
	got, err := domain.DecodeBundleEnvelope(mustRead(t, s.path))
	if err != nil {
		t.Fatalf("落盘的文件解不开: %v", err)
	}
	if got.Generation != "gen-1" {
		t.Errorf("generation = %q，期望 gen-1", got.Generation)
	}
	if got.Node.ID != "1245816621804034" || got.Node.Interface != "gnb0" {
		t.Errorf("node 字段丢失: %+v", got.Node)
	}
	if s.ETag() != `"etag-1"` {
		t.Errorf("etag 未持久化，读回 %q", s.ETag())
	}
	// 路径口径：必须就是 mynet refresh-* 读的那个位置
	if filepath.Base(s.path) != "bundle.json" || filepath.Base(filepath.Dir(s.path)) != "var" {
		t.Errorf("落盘路径 %s 不是 <MYNET_HOME>/var/bundle.json —— 执行器读不到", s.path)
	}
	if _, err := os.Stat(s.path + ".tmp"); !os.IsNotExist(err) {
		t.Error("临时文件没被 rename 掉，留下了 .tmp")
	}
}

// 不校验就写盘会把好配置换成垃圾，而且下次条件请求还带着旧 etag 拿 304，
// 于是节点**永久**卡在坏配置上。这条测的就是那个不可逆。
func TestBundleSaveRefusesGarbageAndKeepsGoodCopy(t *testing.T) {
	s := tempStore(t)
	good := validEnvelope(t, "gen-good")
	if err := s.Save(good, `"etag-good"`); err != nil {
		t.Fatalf("Save(good): %v", err)
	}
	before := mustRead(t, s.path)

	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"back 的业务错误体", []byte(`{"success":false,"error":"bundle 获取失败"}`)},
		{"relay/nginx 的 HTML 错误页", []byte(`<html><head><title>502 Bad Gateway</title></head></html>`)},
		{"空体", []byte(``)},
		{"截断的 JSON", validEnvelope(t, "gen-x")[:40]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Save(tc.body, `"etag-bad"`); err == nil {
				t.Fatal("非法体竟然被接受了 —— 会覆盖掉好配置")
			}
			if after := mustRead(t, s.path); string(after) != string(before) {
				t.Error("好配置被改动了")
			}
			if s.ETag() != `"etag-good"` {
				t.Errorf("etag 被写成 %q —— 下次会拿它去条件请求，于是永久卡在坏配置", s.ETag())
			}
		})
	}
}

// ── Sync：200 / 304 / 错误 ───────────────────────────────────

func TestBundleSyncUpdatedThenUnchanged(t *testing.T) {
	s := tempStore(t)
	body := validEnvelope(t, "gen-1")
	var seenINM []string

	fetch := func(_ context.Context, ifNoneMatch string) (int, string, []byte, error) {
		seenINM = append(seenINM, ifNoneMatch)
		if ifNoneMatch == "" {
			return http.StatusOK, `"etag-1"`, body, nil
		}
		return http.StatusNotModified, `"etag-1"`, nil, nil
	}

	status, err := s.Sync(context.Background(), fetch)
	if err != nil || status != "updated" {
		t.Fatalf("首拉 = (%q, %v)，期望 (updated, nil)", status, err)
	}
	first := mustRead(t, s.path)

	// 第二次必须带上刚存下的 etag，且 304 时**不动文件**
	status, err = s.Sync(context.Background(), fetch)
	if err != nil || status != "unchanged" {
		t.Fatalf("二拉 = (%q, %v)，期望 (unchanged, nil) —— 304 不是失败", status, err)
	}
	if len(seenINM) != 2 || seenINM[0] != "" || seenINM[1] != `"etag-1"` {
		t.Errorf("If-None-Match 序列 = %q，期望 [\"\" `\"etag-1\"`]", seenINM)
	}
	if string(mustRead(t, s.path)) != string(first) {
		t.Error("304 却改动了 bundle.json")
	}
}

// 拉取失败不能把本地缓存搞坏：节点可能正靠上一版配置跑着。
func TestBundleSyncErrorLeavesCacheAlone(t *testing.T) {
	s := tempStore(t)
	good := validEnvelope(t, "gen-good")
	if err := s.Save(good, `"etag-good"`); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before := mustRead(t, s.path)

	for _, tc := range []struct {
		name string
		code int
		body []byte
	}{
		{"401（签名/密钥不对）", http.StatusUnauthorized, []byte(`{"message":"Unauthenticated."}`)},
		{"500", http.StatusInternalServerError, []byte(`{"success":false}`)},
		{"404（节点不存在）", http.StatusNotFound, []byte(`{"success":false,"error":"Node not found"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Sync(context.Background(), func(context.Context, string) (int, string, []byte, error) {
				return tc.code, "", tc.body, nil
			})
			if err == nil {
				t.Fatalf("%d 竟然没报错", tc.code)
			}
			if !strings.Contains(err.Error(), strings.TrimSpace(tc.name[:3])) {
				t.Errorf("错误信息里没有状态码：%v", err)
			}
			if string(mustRead(t, s.path)) != string(before) {
				t.Error("失败却改动了本地缓存")
			}
		})
	}
}

// ── fetchBundle：真 HTTP + 真签名 ────────────────────────────

// 这条测的是**签名口径**：path 差一个前导斜杠就全 401，而症状是「鉴权失败」
// 不是「路径写错」（backclient.go 顶部记的那个坑）。所以既要断言 path 字面值，
// 也要用真 signer 反验一次。
func TestFetchBundleSignsPathAndForwardsConditional(t *testing.T) {
	var gotMethod, gotPath, gotTS, gotSig, gotNode, gotINM string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Query().Get("path")
		gotTS = r.Header.Get("X-Timestamp")
		gotSig = r.Header.Get("X-Node-Signature")
		gotNode = r.Header.Get("X-Node-Id")
		gotINM = r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"etag-srv"`)
		w.Header().Set("Cache-Control", "private, no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(validEnvelope(t, "gen-srv"))
	}))
	defer srv.Close()

	ident, err := loadNodeIdentity(writeConf(t, validConf()))
	if err != nil {
		t.Fatal(err)
	}
	bc := &backClient{relayBase: srv.URL + "/v1/relay", id: ident, hc: srv.Client()}

	code, etag, body, err := bc.fetchBundle(context.Background(), `"etag-prev"`)
	if err != nil {
		t.Fatalf("fetchBundle: %v", err)
	}
	if code != http.StatusOK {
		t.Errorf("code = %d，期望 200", code)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %s，期望 GET", gotMethod)
	}
	if gotPath != "/api/v1/monitor/node/bundle" {
		t.Errorf("path = %q，期望 /api/v1/monitor/node/bundle（差一个斜杠就全 401）", gotPath)
	}
	if gotNode != ident.nodeID {
		t.Errorf("X-Node-Id = %q，期望 %q", gotNode, ident.nodeID)
	}
	if gotINM != `"etag-prev"` {
		t.Errorf("If-None-Match = %q，期望原样透传", gotINM)
	}
	if etag != `"etag-srv"` {
		t.Errorf("etag = %q，期望从响应头读到", etag)
	}
	// GET 无体，签的必须是空体的 sha256
	if !ident.signer.Verify(gotMethod, gotPath, gotTS, "", gotSig) {
		t.Error("用空体验签失败 —— 签名口径不对，back 侧会 401")
	}
	if _, err := domain.DecodeBundleEnvelope(body); err != nil {
		t.Errorf("响应体解不开: %v", err)
	}
}

// 304 是正常结果，不是错误。把它当错误会让「配置没变」在日志里看起来像故障。
func TestFetchBundleHandles304(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", r.Header.Get("If-None-Match"))
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	ident, err := loadNodeIdentity(writeConf(t, validConf()))
	if err != nil {
		t.Fatal(err)
	}
	bc := &backClient{relayBase: srv.URL + "/v1/relay", id: ident, hc: srv.Client()}

	code, etag, body, err := bc.fetchBundle(context.Background(), `"etag-9"`)
	if err != nil {
		t.Fatalf("304 不该报错: %v", err)
	}
	if code != http.StatusNotModified {
		t.Errorf("code = %d，期望 304", code)
	}
	if len(body) != 0 {
		t.Errorf("304 却带了 %d 字节体", len(body))
	}
	if etag != `"etag-9"` {
		t.Errorf("etag = %q", etag)
	}
}

// ── 事件：bundle_changed 必须「先拉后刷」─────────────────────
// 日志收集用 events_test.go 里既有的 logSink / waitLog（同一包，不重复造）。

func TestBundleChangedPullsBeforeRefresh(t *testing.T) {
	ex := &recordingExec{}
	sink := &logSink{}
	d := newEventDispatcher("n1", ex.run, nil, nil)
	d.logf = sink.printf

	var order []string
	var mu sync.Mutex
	d.setBundleSyncer(func(context.Context) (string, error) {
		mu.Lock()
		order = append(order, "sync")
		mu.Unlock()
		return "updated", nil
	})
	// exec 侧也记一笔顺序：只有「sync 在 exec 之前」才说明真的先拉后刷
	origRun := ex.run
	d.exec = func(argv []string) (string, string, error) {
		mu.Lock()
		order = append(order, "exec")
		mu.Unlock()
		return origRun(argv)
	}

	d.Handle("bundle_changed", nil)
	if !ex.waitCalls(t, 1) {
		t.Fatalf("refresh 没被触发；日志：%s", sink.joined())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "sync" || order[1] != "exec" {
		t.Errorf("执行顺序 = %v，期望 [sync exec] —— 先刷后拉等于刷了个旧配置", order)
	}
	calls := ex.snapshot()
	if !strings.Contains(calls[0], "refresh-config") {
		t.Errorf("argv = %q，期望映射到 refresh-config", calls[0])
	}
	if !sink.waitLog(t, "bundle updated") {
		t.Errorf("日志没说明拉取结果；日志：%s", sink.joined())
	}
}

// 拉取失败时**不该**去跑 refresh：那样报出来的是「读 bundle 失败」，
// 会把「上游没拉到」和「本地刷新坏了」两种故障混成一条日志。
func TestBundleChangedSkipsRefreshWhenSyncFails(t *testing.T) {
	ex := &recordingExec{}
	sink := &logSink{}
	d := newEventDispatcher("n1", ex.run, nil, nil)
	d.logf = sink.printf
	d.setBundleSyncer(func(context.Context) (string, error) {
		return "", context.DeadlineExceeded
	})

	d.Handle("bundle_changed", nil)
	if !sink.waitLog(t, "bundle 拉取失败") {
		t.Fatalf("没记录拉取失败；日志：%s", sink.joined())
	}
	if n := len(ex.snapshot()); n != 0 {
		t.Errorf("拉取失败却仍跑了 %d 次 refresh", n)
	}
}

// 未装配（没配 gateway / 无节点身份）时必须**说清缺什么**，
// 而不是静默忽略 —— 静默的症状是「back 改了配置但节点毫无反应」。
func TestBundleChangedWithoutSyncerLogsWhatIsMissing(t *testing.T) {
	ex := &recordingExec{}
	sink := &logSink{}
	d := newEventDispatcher("n1", ex.run, nil, nil)
	d.logf = sink.printf

	d.Handle("bundle_changed", nil)
	if !sink.waitLog(t, "bundle 拉取链未装配") {
		t.Fatalf("没说明缺什么；日志：%s", sink.joined())
	}
	if n := len(ex.snapshot()); n != 0 {
		t.Errorf("未装配却跑了 %d 次 refresh", n)
	}
}

// route_changed 不依赖 back 下发的配置包，**不该**触发拉取。
func TestRouteChangedDoesNotPullBundle(t *testing.T) {
	ex := &recordingExec{}
	sink := &logSink{}
	d := newEventDispatcher("n1", ex.run, nil, nil)
	d.logf = sink.printf
	pulled := false
	d.setBundleSyncer(func(context.Context) (string, error) {
		pulled = true
		return "updated", nil
	})

	d.Handle("route_changed", nil)
	if !ex.waitCalls(t, 1) {
		t.Fatalf("refresh-routes 没被触发；日志：%s", sink.joined())
	}
	if pulled {
		t.Error("route_changed 触发了 bundle 拉取 —— 它不需要配置包")
	}
	if !strings.Contains(ex.snapshot()[0], "refresh-routes") {
		t.Errorf("argv = %q，期望 refresh-routes", ex.snapshot()[0])
	}
}
