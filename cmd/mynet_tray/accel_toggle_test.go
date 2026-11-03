package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// accelStub 按 **localapi 的真实方法约束** 回放加速 API：
// /api/v1/acceleration 只收 GET|PUT（POST 405），install/apply 只收 POST。
// 旧托盘用 POST 保存配置，405 被当成功吞掉 ⇒「启动加速没用」，本文件是回归锁。
type accelStub struct {
	statusBody string
	// configCode 覆盖 PUT /api/v1/acceleration 的响应码（0 = 200）。
	configCode int
	// configFail 时 PUT 返回 200 + success:false（业务失败信封）。
	configFail bool
	applyCode  int
	applyFail  bool
	installErr bool

	configMethod string
	configBody   string
	applyHits    int
	installHits  int
}

func (s *accelStub) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/acceleration/status":
			_, _ = io.WriteString(w, s.statusBody)
		case "/api/v1/acceleration/install":
			s.installHits++
			if s.installErr {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"success":false,"message":"PC_RUNTIME_UNAVAILABLE"}`)
				return
			}
			_, _ = io.WriteString(w, `{"success":true,"data":{"runtime":"pc"}}`)
		case "/api/v1/acceleration":
			// 真实 handleAcceleration：POST 直接 405（acceleration_api.go「GET|PUT only」）。
			if r.Method != http.MethodPut {
				w.WriteHeader(http.StatusMethodNotAllowed)
				_, _ = io.WriteString(w, `{"success":false,"message":"GET|PUT only"}`)
				return
			}
			s.configMethod = r.Method
			raw, _ := io.ReadAll(r.Body)
			s.configBody = string(raw)
			if s.configFail {
				_, _ = io.WriteString(w, `{"success":false,"message":"桌面加速尚未配置"}`)
				return
			}
			code := s.configCode
			if code == 0 {
				code = http.StatusOK
			}
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"success":true,"data":{}}`)
		case "/api/v1/acceleration/apply":
			s.applyHits++
			code := s.applyCode
			if code == 0 {
				code = http.StatusOK
			}
			w.WriteHeader(code)
			if s.applyFail {
				_, _ = io.WriteString(w, `{"success":false,"reason_code":"DNS_HOOK_APPLY_FAILED","message":"dns-hook start 失败"}`)
				return
			}
			_, _ = io.WriteString(w, `{"success":true,"data":{}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"success":false,"message":"unexpected path"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// accelStatusFixture 覆盖 toggleAccel 解析所需字段：config.enabled / runtime.active。
func accelStatusFixture(enabled, active bool) string {
	b, _ := json.Marshal(map[string]any{
		"success": true,
		"data": map[string]any{
			"config": map[string]any{
				"enabled":        enabled,
				"dns_enabled":    false,
				"dns_sources":    []string{"custom", "tenant", "peer", "clean_public"},
				"custom_dns":     []string{},
				"custom_domains": []string{},
			},
			"runtime": map[string]any{"active": active, "state": "disabled"},
		},
	})
	return string(b)
}

func noticeText(t *testing.T, a *App) string {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.noticeText
}

// TestToggleAccelSavesConfigWithPUT 锁「配置保存走 PUT」——
// /api/v1/acceleration 只收 GET|PUT，POST 405 曾被当成功吞掉（托盘启动加速没用）。
func TestToggleAccelSavesConfigWithPUT(t *testing.T) {
	isolateUserConfig(t)
	stub := &accelStub{statusBody: accelStatusFixture(false, false)}
	srv := stub.serve(t)
	a, _ := testApp(t, strings.TrimPrefix(srv.URL, "http://"), &fakeProber{})

	a.toggleAccel()

	if stub.configMethod != http.MethodPut {
		t.Fatalf("配置保存方法 = %q，必须 PUT（POST 会被 405 拒）", stub.configMethod)
	}
	var cfg struct {
		Enabled    bool `json:"enabled"`
		DNSEnabled bool `json:"dns_enabled"`
	}
	if err := json.Unmarshal([]byte(stub.configBody), &cfg); err != nil {
		t.Fatalf("配置 body 不是 JSON: %v (%s)", err, stub.configBody)
	}
	if !cfg.Enabled || !cfg.DNSEnabled {
		t.Errorf("开启加速应写 enabled=true 且 dns_enabled=true，实际 %s", stub.configBody)
	}
	if stub.installHits != 1 {
		t.Errorf("开启加速应先 install（PC runtime），实际 %d 次", stub.installHits)
	}
	if stub.applyHits != 1 {
		t.Errorf("保存后应 apply 一次，实际 %d 次", stub.applyHits)
	}
	if got := noticeText(t, a); !strings.Contains(got, "加速设置已更新") {
		t.Errorf("全流程成功应提示「加速设置已更新」，实际 %q", got)
	}
}

// TestToggleAccelReportsConfigSaveFailure 错误可见性：
// 保存被拒时必须提示失败，不得再报「已更新」。
func TestToggleAccelReportsConfigSaveFailure(t *testing.T) {
	isolateUserConfig(t)
	stub := &accelStub{statusBody: accelStatusFixture(false, false), configFail: true}
	srv := stub.serve(t)
	a, _ := testApp(t, strings.TrimPrefix(srv.URL, "http://"), &fakeProber{})

	a.toggleAccel()

	got := noticeText(t, a)
	if !strings.Contains(got, "保存加速设置失败") {
		t.Errorf("保存失败必须可见，实际提示 %q", got)
	}
	if strings.Contains(got, "加速设置已更新") {
		t.Errorf("保存失败却提示已更新: %q", got)
	}
	if stub.applyHits != 0 {
		t.Errorf("保存失败后不得再 apply，实际 %d 次", stub.applyHits)
	}
}

// TestToggleAccelReportsApplyFailure apply 失败要带上「开启/关闭失败」，不能吞。
func TestToggleAccelReportsApplyFailure(t *testing.T) {
	isolateUserConfig(t)
	stub := &accelStub{statusBody: accelStatusFixture(false, false), applyFail: true}
	srv := stub.serve(t)
	a, _ := testApp(t, strings.TrimPrefix(srv.URL, "http://"), &fakeProber{})

	a.toggleAccel()

	got := noticeText(t, a)
	if !strings.Contains(got, "加速开启失败") {
		t.Errorf("apply 失败必须提示「加速开启失败」，实际 %q", got)
	}
	if strings.Contains(got, "加速设置已更新") {
		t.Errorf("apply 失败却提示已更新: %q", got)
	}
}

// TestToggleAccelReportsInstallFailure install 失败即止，不得继续保存。
func TestToggleAccelReportsInstallFailure(t *testing.T) {
	isolateUserConfig(t)
	stub := &accelStub{statusBody: accelStatusFixture(false, false), installErr: true}
	srv := stub.serve(t)
	a, _ := testApp(t, strings.TrimPrefix(srv.URL, "http://"), &fakeProber{})

	a.toggleAccel()

	got := noticeText(t, a)
	if !strings.Contains(got, "安装本机加速失败") {
		t.Errorf("install 失败必须可见，实际提示 %q", got)
	}
	if stub.configMethod != "" {
		t.Errorf("install 失败后不得保存配置，实际 %s %s", stub.configMethod, stub.configBody)
	}
}

// TestToggleAccelStopKeepsIntentOff 关闭路径写 enabled=false（不残留开启意图）。
func TestToggleAccelStopKeepsIntentOff(t *testing.T) {
	isolateUserConfig(t)
	stub := &accelStub{statusBody: accelStatusFixture(true, true)}
	srv := stub.serve(t)
	a, _ := testApp(t, strings.TrimPrefix(srv.URL, "http://"), &fakeProber{})

	a.toggleAccel()

	var cfg struct {
		Enabled    bool `json:"enabled"`
		DNSEnabled bool `json:"dns_enabled"`
	}
	if err := json.Unmarshal([]byte(stub.configBody), &cfg); err != nil {
		t.Fatalf("配置 body 不是 JSON: %v (%s)", err, stub.configBody)
	}
	if cfg.Enabled || cfg.DNSEnabled {
		t.Errorf("关闭加速应写 enabled=false 且 dns_enabled=false，实际 %s", stub.configBody)
	}
	if stub.installHits != 0 {
		t.Errorf("关闭路径不该 install，实际 %d 次", stub.installHits)
	}
	if stub.applyHits != 1 {
		t.Errorf("关闭也应 apply 一次，实际 %d 次", stub.applyHits)
	}
}
