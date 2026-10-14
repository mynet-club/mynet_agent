package mynet

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// G3 缺口⑤：`mynet config keys rotate` 换钥一条龙。
// generate → sync（自动带新指纹+重绑）→ pull（补拉新修订）→ refresh-config。
// 网络环（sync/pull）打桩；generate 走真的（keys 写盘可断言）。

// stubStep 一环的替身：记录调用次序，吐一个 JSON（同真环的 stdout 形态）。
type stubStep struct {
	name string
	code int
	log  *[]string
}

func (s stubStep) fn(args []string) int {
	*s.log = append(*s.log, s.name)
	_ = args
	_ = printJSON(map[string]any{"step": s.name})
	return s.code
}

func stubSteps(log *[]string, fail map[string]int) rotateSteps {
	mk := func(name string) func([]string) int {
		return stubStep{name: name, code: fail[name], log: log}.fn
	}
	return rotateSteps{generate: mk("generate"), sync: mk("sync"), pull: mk("pull"), refresh: mk("refresh")}
}

// 四环全过：次序对、stdout 恰好一个 JSON（localapi 契约）、rotated=true。
func TestConfigKeysRotateRunsAllStepsInOrder(t *testing.T) {
	var order []string
	var rc int
	out := captureStdoutFn(t, func() {
		rc = configFaceKeysRotateRun(nil, stubSteps(&order, nil))
	})
	if rc != 0 {
		t.Fatalf("全过应退出 0，实际 %d", rc)
	}
	if want := "generate,sync,pull,refresh"; strings.Join(order, ",") != want {
		t.Fatalf("环次序错: %v", order)
	}
	if !json.Valid([]byte(strings.TrimSpace(out))) {
		t.Fatalf("stdout 必须是恰好一个 JSON:\n%s", out)
	}
	var res struct {
		Rotated bool                      `json:"rotated"`
		Steps   map[string]map[string]any `json:"steps"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Rotated {
		t.Fatalf("全过应 rotated=true:\n%s", out)
	}
	for _, k := range []string{"generate", "sync", "pull", "refresh"} {
		if _, ok := res.Steps[k]; !ok {
			t.Fatalf("steps 缺 %q:\n%s", k, out)
		}
	}
}

// 任一环失败：fail loud —— 非零退出、明说卡在哪一环、后续环不跑。
func TestConfigKeysRotateFailsLoudAtFailedStep(t *testing.T) {
	var order []string
	var rc int
	var out string
	stderr := captureStderrFn(t, func() {
		out = captureStdoutFn(t, func() {
			rc = configFaceKeysRotateRun(nil, stubSteps(&order, map[string]int{"sync": 3}))
		})
	})

	if rc != 3 {
		t.Fatalf("失败退出码应沿用该环的非零码 3，实际 %d", rc)
	}
	if want := "generate,sync"; strings.Join(order, ",") != want {
		t.Fatalf("失败后不许再跑后续环（pull/refresh 一次都不该出现）: %v", order)
	}

	var res struct {
		Error      string `json:"error"`
		Message    string `json:"message"`
		FailedStep string `json:"failed_step"`
		StepIndex  int    `json:"step_index"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("失败也要出一个 JSON: %v\n%s", err, out)
	}
	if res.Error != "rotate_failed" || res.FailedStep != "sync" || res.StepIndex != 2 {
		t.Fatalf("失败归因错: %+v\n%s", res, out)
	}
	if !strings.Contains(res.Message, "卡在第 2 环") {
		t.Fatalf("message 该明说卡在哪一环: %q", res.Message)
	}
	if !strings.Contains(stderr, "卡在第 2 环") {
		t.Fatalf("stderr 也要明说卡在哪一环:\n%s", stderr)
	}
}

// generate 环走真的：密钥落盘（节点唯一存放位）且 steps.generate 里有公钥。
// sync/pull/refresh 是网络/渲染环，打桩（G3 DoD 允许 rotate 测试打桩 sync）。
func TestConfigKeysRotateRealGenerateStep(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MYNET_HOME", root)
	if rc := cmdSetupV2([]string{"--local", "--node-id", "3001"}); rc != 0 {
		t.Fatalf("setup rc=%d", rc)
	}

	var order []string
	var rc int
	st := stubSteps(&order, nil)
	st.generate = configFaceKeysGenerate // generate 走真的，其余环打桩
	out := captureStdoutFn(t, func() {
		rc = configFaceKeysRotateRun(
			[]string{"--vpn", "gnb", "--node-id", "3001"}, st)
	})
	if rc != 0 {
		t.Fatalf("应成功，实际 rc=%d\n%s", rc, out)
	}

	var res struct {
		Rotated bool `json:"rotated"`
		Steps   struct {
			Generate map[string]any `json:"generate"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Rotated {
		t.Fatalf("应成功:\n%s", out)
	}
	pub, _ := res.Steps.Generate["public_hex"].(string)
	if pub == "" {
		t.Fatalf("generate 环应给出 public_hex:\n%s", out)
	}
	if got, _ := res.Steps.Generate["node_id"].(string); got != "3001" {
		t.Fatalf("node_id 归因错: %v", res.Steps.Generate)
	}
	privPath := filepath.Join(root, "conf", "local", "3001", "security", "3001.private")
	if _, err := os.Stat(privPath); err != nil {
		t.Fatalf("新私钥没落盘 %s: %v", privPath, err)
	}
}

// captureStdoutFn / captureStderrFn 收回 f 写到 stdout/stderr 的全部输出。
// 与 cmd_config_face_keys_test.go 的 captureStdout 区别：f 无返回值，
// 退出码由闭包变量携带（rotate 的 rc 归因要断言）。
func captureStdoutFn(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	f()
	_ = w.Close()
	os.Stdout = old
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func captureStderrFn(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	f()
	_ = w.Close()
	os.Stderr = old
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
