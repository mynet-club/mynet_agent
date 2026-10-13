package localapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// C1-S1 回归：argv 里的秘密值必须遮蔽（--private/--password/--token/--secret 类），
// 公钥等非秘密参数保持可见（redactSecrets 的「不误杀公钥」原则同样适用于 argv）。
func TestRedactArgvMasksSecretValues(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "--private 值遮蔽",
			in:   []string{"config", "keys", "set", "--json", "--private", "deadbeefcafebabe"},
			want: []string{"config", "keys", "set", "--json", "--private", "********"},
		},
		{
			name: "--private= 等值形态遮蔽",
			in:   []string{"config", "keys", "set", "--private=deadbeefcafebabe"},
			want: []string{"config", "keys", "set", "--private=********"},
		},
		{
			name: "--password 值遮蔽",
			in:   []string{"auth", "login", "--email", "a@b.c", "--password", "hunter2"},
			want: []string{"auth", "login", "--email", "a@b.c", "--password", "********"},
		},
		{
			name: "--token / --secret 值遮蔽",
			in:   []string{"x", "--token", "tok123", "--secret", "s3c"},
			want: []string{"x", "--token", "********", "--secret", "********"},
		},
		{
			name: "--private-stdin 不吞下一个参数",
			in:   []string{"config", "keys", "set", "--private-stdin", "--node-id", "n1"},
			want: []string{"config", "keys", "set", "--private-stdin", "--node-id", "n1"},
		},
		{
			name: "非秘密参数不误杀",
			in:   []string{"config", "keys", "set", "--json", "--public", "aabbcc", "--node-id", "n1"},
			want: []string{"config", "keys", "set", "--json", "--public", "aabbcc", "--node-id", "n1"},
		},
	}
	for _, c := range cases {
		got := redactArgv(c.in)
		if strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// sanitizeWizardOut 对 argv 同样遮蔽（此前显式放行 argv）。
func TestSanitizeWizardOutMasksArgvSecrets(t *testing.T) {
	secret := strings.Repeat("de", 8) // 非凭据，纯测试值（避免 A1 误报）
	out := sanitizeWizardOut(map[string]any{
		"argv":   []string{"config", "keys", "set", "--private", secret},
		"stdout": "ok",
	})
	argv, _ := out["argv"].([]string)
	if strings.Contains(strings.Join(argv, " "), secret) {
		t.Fatalf("sanitizeWizardOut 未遮蔽 argv 秘密: %q", argv)
	}
	if argv[3] != "--private" || argv[4] != "********" {
		t.Fatalf("argv 形态不对: %q", argv)
	}
}

// 失败路径（dispatch 的 500 响应）不得回显 argv 私密值 —— C1-S1 明列的洞。
func TestDispatchFailureMasksArgvSecrets(t *testing.T) {
	secret := strings.Repeat("de", 8) // 非凭据，纯测试值（避免 A1 误报）
	s := NewServer(Config{Listen: "127.0.0.1:0", Token: "t"})
	s.Ops = func(argv []string) (any, error) {
		return map[string]any{
			"argv":   argv,
			"stdout": "",
			"stderr": "boom",
			"error":  "exit status 1",
		}, errors.New("exit status 1")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/command",
		strings.NewReader(`{"argv":["config","keys","set","--json","--private","`+secret+`"]}`))
	req.Host = "127.0.0.1:8096"
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("失败应 500: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("失败响应回显了 argv 私密值: %s", rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	argv, _ := body["argv"].([]any)
	if len(argv) == 0 {
		t.Fatalf("应回 argv（遮蔽后）: %v", body)
	}
	last, _ := argv[len(argv)-1].(string)
	if last != "********" {
		t.Fatalf("argv 末值应被遮蔽: %q", argv)
	}
}
