package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeBack 起一个假 back，记录收到的请求以便断言透传是否正确。
type fakeBack struct {
	*httptest.Server
	mu       chan struct{}
	lastPath string
	lastBody string
	lastAuth string
	lastTen  string
	lastMeth string
}

func newFakeBack(t *testing.T, status int, body string, extraHeaders http.Header) *fakeBack {
	t.Helper()
	fb := &fakeBack{mu: make(chan struct{}, 8)}
	fb.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		fb.lastPath = r.URL.RequestURI()
		fb.lastBody = string(raw)
		fb.lastAuth = r.Header.Get("Authorization")
		fb.lastTen = r.Header.Get("X-Tenant-ID")
		fb.lastMeth = r.Method
		for k, vs := range extraHeaders {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
		select {
		case fb.mu <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(fb.Close)
	return fb
}

// waitHit 等假 back 真被打到，避免用 sleep 猜。
func (fb *fakeBack) waitHit(t *testing.T) bool {
	t.Helper()
	select {
	case <-fb.mu:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

func relayURL(base, path string) string {
	return base + "/v1/relay?path=" + url.QueryEscape(path)
}

// 白名单内的路径必须真转发到上游，并把状态码与 body 原样回写。
func TestRelayForwardsAllowedPath(t *testing.T) {
	fb := newFakeBack(t, 200, `{"success":true,"data":[{"node_id":1}]}`, nil)
	s := NewServer(fb.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(relayURL(ts.URL, "/api/v1/nodes"))
	if err != nil {
		t.Fatalf("GET relay: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("relay 状态码 = %d, want 200（body: %s）", resp.StatusCode, got)
	}
	if !strings.Contains(string(got), `"node_id":1`) {
		t.Errorf("上游 body 未原样回写: %s", got)
	}
	if !fb.waitHit(t) {
		t.Fatal("上游根本没被打到 —— relay 没有真转发")
	}
	if fb.lastPath != "/api/v1/nodes" {
		t.Errorf("上游收到的路径 = %q, want /api/v1/nodes", fb.lastPath)
	}
}

// method / body / 关键头必须透传，否则登录这类 POST 无法工作。
func TestRelayPassesThroughMethodBodyAndHeaders(t *testing.T) {
	fb := newFakeBack(t, 200, `{"success":true}`, nil)
	s := NewServer(fb.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, relayURL(ts.URL, "/api/v1/auth/login"),
		strings.NewReader(`{"email":"a@b.c","password":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok-123")
	req.Header.Set("X-Tenant-ID", "1000000000000001")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST relay: %v", err)
	}
	defer resp.Body.Close()

	if !fb.waitHit(t) {
		t.Fatal("上游没被打到")
	}
	if fb.lastMeth != http.MethodPost {
		t.Errorf("上游收到 method = %q, want POST", fb.lastMeth)
	}
	if !strings.Contains(fb.lastBody, `"email":"a@b.c"`) {
		t.Errorf("body 未透传，上游收到 %q", fb.lastBody)
	}
	if fb.lastAuth != "Bearer tok-123" {
		t.Errorf("Authorization 未透传，上游收到 %q", fb.lastAuth)
	}
	if fb.lastTen != "1000000000000001" {
		t.Errorf("X-Tenant-ID 未透传，上游收到 %q", fb.lastTen)
	}
}

// 上游的 4xx 必须原样透传，不能被 gateway 改写成 502——否则客户端分不清
// 「back 拒绝了我」和「gateway 连不上 back」。
func TestRelayPropagatesUpstreamErrorStatus(t *testing.T) {
	fb := newFakeBack(t, 401, `{"success":false,"message":"Unauthenticated."}`, nil)
	s := NewServer(fb.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(relayURL(ts.URL, "/api/v1/nodes"))
	if err != nil {
		t.Fatalf("GET relay: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("上游 401 应原样透传，实际 %d", resp.StatusCode)
	}
}

// B3：白名单外的路径一律 403，且**上游不得被打到**。
func TestRelayDeniesNonWhitelistedPath(t *testing.T) {
	fb := newFakeBack(t, 200, `{"success":true}`, nil)
	s := NewServer(fb.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	for _, bad := range []string{
		"/api/v1/auth/admin/settings", // deniedFragments: /auth/admin
		"/admin/dashboard",            // deniedFragments: /admin
		"/api/v1/secret",              // 不在 AllowedPathPrefixes
		"/api/v2/nodes",               // 版本不对，不在白名单
	} {
		resp, err := http.Get(relayURL(ts.URL, bad))
		if err != nil {
			t.Fatalf("GET relay(%s): %v", bad, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("path=%s 应 403，实际 %d（%s）", bad, resp.StatusCode, body)
		}
	}

	select {
	case <-fb.mu:
		t.Error("白名单外的请求竟然打到了上游 —— 拒绝必须发生在转发之前")
	default:
	}
}

// 含 % 的路径一律拒（防 %2f 绕过）。这条不能被「优化」掉。
func TestRelayRejectsPercentEncodedPath(t *testing.T) {
	fb := newFakeBack(t, 200, `{"success":true}`, nil)
	s := NewServer(fb.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// 直接构造未编码的 query，确保 % 真的到达 handler
	raw := ts.URL + "/v1/relay?path=/api/v1/nodes%2f..%2fadmin"
	resp, err := http.Get(raw)
	if err != nil {
		t.Fatalf("GET relay: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("含 %% 的路径应 403，实际 %d", resp.StatusCode)
	}
}

func TestRelayRequiresPathParam(t *testing.T) {
	fb := newFakeBack(t, 200, `{}`, nil)
	s := NewServer(fb.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/relay")
	if err != nil {
		t.Fatalf("GET relay: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("缺 path 应 400，实际 %d", resp.StatusCode)
	}
}

// 上游不可达 → 502（而不是挂死或 panic）。
func TestRelayUpstreamUnreachableIs502(t *testing.T) {
	// 占一个端口再立刻关掉，得到一个确定无人监听的地址
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	s := NewServer(deadURL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(relayURL(ts.URL, "/api/v1/nodes"))
	if err != nil {
		t.Fatalf("GET relay: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("上游不可达应 502，实际 %d", resp.StatusCode)
	}
}

// C-4 闭环：back 在响应头写 X-Node-Verified，gateway 解析后把该连接标为已鉴权。
// 这条串起了 relay + withVerifiedHook + Registry，是「伪造 node_id 不被置位」的正面对照。
func TestRelayUpstreamVerifiedHeaderMarksConn(t *testing.T) {
	fb := newFakeBack(t, 200, `{"success":true}`, http.Header{
		VerifiedHeader: []string{"n-relay"},
	})
	s := NewServer(fb.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/node/connect"
	conn := dialNode(t, wsURL, "n-relay")
	defer conn.Close(websocket.StatusNormalClosure, "bye")
	waitForConns(t, s, 1)

	if s.Registry().IsVerified("n-relay") {
		t.Fatal("起始必须是未鉴权")
	}

	resp, err := http.Get(relayURL(ts.URL, "/api/v1/nodes"))
	if err != nil {
		t.Fatalf("GET relay: %v", err)
	}
	defer resp.Body.Close()
	if !fb.waitHit(t) {
		t.Fatal("上游没被打到")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.Registry().IsVerified("n-relay") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("上游回了 X-Node-Verified 但连接未被置位 —— C-4 闭环断了")
}

// SSRF 纵深防御：即便有路径绕过了白名单，解析后逃逸到别的主机也必须被拒。
// 直接测 resolveUpstream，因为经 handler 时白名单会先挡掉这类输入。
func TestResolveUpstreamRejectsHostEscape(t *testing.T) {
	s := NewServer("http://10.0.0.5:8080", "")

	ok, err := s.resolveUpstream("/api/v1/nodes")
	if err != nil {
		t.Fatalf("正常路径不应报错: %v", err)
	}
	if ok.Host != "10.0.0.5:8080" {
		t.Errorf("正常路径 host = %q", ok.Host)
	}

	for _, evil := range []string{
		"//evil.com/api/v1/nodes",
		"http://evil.com/api/v1/nodes",
		"https://evil.com/api/v1/nodes",
	} {
		if got, err := s.resolveUpstream(evil); err == nil {
			t.Errorf("path=%q 应被拒，实际解析成 %s", evil, got)
		}
	}
}

func TestResolveUpstreamRejectsBadUpstream(t *testing.T) {
	s := NewServer("not a url", "")
	if _, err := s.resolveUpstream("/api/v1/nodes"); err == nil {
		t.Error("非法 upstream 应报错")
	}

	s2 := NewServer("/only/a/path", "")
	if _, err := s2.resolveUpstream("/api/v1/nodes"); err == nil {
		t.Error("缺主机名的 upstream 应报错")
	}
}

// 带 query 的 path 必须把 query 一起带给上游（分页、过滤都靠它）。
func TestRelayPreservesUpstreamQuery(t *testing.T) {
	fb := newFakeBack(t, 200, `{"success":true}`, nil)
	s := NewServer(fb.URL, "")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(relayURL(ts.URL, "/api/v1/nodes?page=2&per_page=50"))
	if err != nil {
		t.Fatalf("GET relay: %v", err)
	}
	defer resp.Body.Close()
	if !fb.waitHit(t) {
		t.Fatal("上游没被打到")
	}
	if !strings.Contains(fb.lastPath, "page=2") || !strings.Contains(fb.lastPath, "per_page=50") {
		t.Errorf("上游收到的 URI 丢了 query: %q", fb.lastPath)
	}
}
