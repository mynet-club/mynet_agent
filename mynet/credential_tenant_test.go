package mynet

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// S2 合同（框架 3.10 EnforcesLoginPolicy）：请求缺 X-Tenant-ID 即 403
// 「无法识别租户上下文」。backClient 有租户必须随请求上行，无租户不发头。
func TestBackClientSendsTenantHeader(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Tenant-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer ts.Close()

	c := newBackClient("")
	c.base = ts.URL
	c.tenant = "1000000000000001"
	if _, _, err := c.doJSON("POST", "/api/v1/auth/login", map[string]string{"email": "a@b.c"}); err != nil {
		t.Fatal(err)
	}
	if got != "1000000000000001" {
		t.Fatalf("X-Tenant-ID=%q want 1000000000000001（S2 显式租户）", got)
	}

	got = "unset"
	c2 := newBackClient("")
	c2.base = ts.URL
	if _, _, err := c2.doJSON("POST", "/api/v1/auth/login", nil); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("空租户不应发 X-Tenant-ID, got %q", got)
	}
}
