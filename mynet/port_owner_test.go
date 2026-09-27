package mynet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// 端口归属三态：空闲 / 自己的旧版 / 外人。

func TestDetectPortOwnerFree(t *testing.T) {
	// 127.0.0.1:1 一般无人监听
	if got := DetectPortOwner("127.0.0.1:1"); got != PortFree {
		t.Fatalf("端口 1 应为 free，得到 %s", got)
	}
}

func TestDetectPortOwnerOurs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/node-status" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"state": "RUNNING", "one_line": "运行中",
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	addr := hostPort(t, srv.URL)

	if got := DetectPortOwner(addr); got != PortOurs {
		t.Fatalf("node-status 应认成 ours，得到 %s", got)
	}
}

func TestDetectPortOwnerForeign(t *testing.T) {
	// 有服务但不是 MyNet 的形状
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>nginx</html>"))
	}))
	defer srv.Close()
	addr := hostPort(t, srv.URL)

	if got := DetectPortOwner(addr); got != PortForeign {
		t.Fatalf("无关服务应为 foreign，得到 %s", got)
	}
}

func TestMustAcceptPort(t *testing.T) {
	// free → nil
	if err := mustAcceptPort("127.0.0.1:1"); err != nil {
		t.Fatalf("free 不应报错: %v", err)
	}
}

func TestIsUpgradeSafePath(t *testing.T) {
	// conf / 身份 不能盖
	for _, p := range []string{"conf/mynet.conf", "var/token/sess", "var/token"} {
		if IsUpgradeSafePath(p) {
			t.Errorf("%s 不该允许覆盖", p)
		}
	}
	// bin 可盖
	for _, p := range []string{"bin/mynet", "bin/mynetd.exe", "webui.html"} {
		if !IsUpgradeSafePath(p) {
			t.Errorf("%s 应允许覆盖", p)
		}
	}
}

func hostPort(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}
