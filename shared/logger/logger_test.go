package logger

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// V4-4 的判据是「关键路径日志可被机器解析」。这条测试就是那个判据本身：
// JSON 模式下**每一行都必须是合法 JSON**，且字段真的带上了。
func TestJSONOutputIsMachineParsable(t *testing.T) {
	var buf bytes.Buffer
	if err := Init(Config{Format: "json", Output: &buf}); err != nil {
		t.Fatal(err)
	}

	Info("节点接入", "node", "1137804926174718", "conns", 3)
	Warn("节点面未就绪", "err", "读节点配置失败")
	Error("heartbeat 上报失败", "node_id", "1137804926174718", "code", 401)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("期望 3 行，得到 %d 行:\n%s", len(lines), buf.String())
	}

	want := []struct {
		level string
		msg   string
		field string
		value any
	}{
		{"INFO", "节点接入", "node", "1137804926174718"},
		{"WARN", "节点面未就绪", "err", "读节点配置失败"},
		{"ERROR", "heartbeat 上报失败", "code", float64(401)},
	}
	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON: %v\n  原文: %s", i+1, err, line)
		}
		if rec["level"] != want[i].level {
			t.Errorf("第 %d 行 level = %v，期望 %s", i+1, rec["level"], want[i].level)
		}
		if rec["msg"] != want[i].msg {
			t.Errorf("第 %d 行 msg = %v，期望 %s", i+1, rec["msg"], want[i].msg)
		}
		if got := rec[want[i].field]; got != want[i].value {
			t.Errorf("第 %d 行字段 %s = %v，期望 %v", i+1, want[i].field, got, want[i].value)
		}
		// 时间戳与源码位置是排查的命根子，必须带上
		if _, ok := rec["time"]; !ok {
			t.Errorf("第 %d 行缺 time", i+1)
		}
		if src, ok := rec["source"].(string); !ok || !strings.Contains(src, "logger_test.go:") {
			t.Errorf("第 %d 行 source = %v，期望形如 logger_test.go:NN", i+1, rec["source"])
		}
	}
}

// text 模式是 logfmt（key=value），同样可被机器解析，但人读着舒服。
// 默认就用它 —— 开发时看终端不至于被一堆引号糊住。
func TestTextOutputIsLogfmt(t *testing.T) {
	var buf bytes.Buffer
	if err := Init(Config{Format: "text", Output: &buf}); err != nil {
		t.Fatal(err)
	}
	Info("gateway 已启动", "listen", "127.0.0.1:8710", "conns", 0)

	line := strings.TrimSpace(buf.String())
	for _, want := range []string{"level=INFO", `msg="gateway 已启动"`, "listen=127.0.0.1:8710", "conns=0"} {
		if !strings.Contains(line, want) {
			t.Errorf("text 输出缺 %q\n  实际: %s", want, line)
		}
	}
	// text 模式**不该**是 JSON（否则等于配错了格式却没人发现）
	if strings.HasPrefix(line, "{") {
		t.Errorf("text 模式却输出了 JSON: %s", line)
	}
}

// 级别过滤：设成 warn 之后 info/debug 不该出现。
// 否则「调低日志量」这个操作是假的，磁盘照样被刷满。
func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	if err := Init(Config{Level: "warn", Format: "json", Output: &buf}); err != nil {
		t.Fatal(err)
	}
	Debug("看不见我")
	Info("也看不见我")
	Warn("这条要出现")
	Error("这条也要")

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("warn 级别下期望 2 行，得到 %d:\n%s", len(lines), buf.String())
	}
	if strings.Contains(buf.String(), "看不见我") {
		t.Error("debug 日志泄漏到了 warn 级别")
	}
}

// SetLevel 要能动态改，且并发安全（原实现改的是无锁字段，-race 会报）。
func TestSetLevelDynamic(t *testing.T) {
	var buf bytes.Buffer
	if err := Init(Config{Level: "error", Format: "json", Output: &buf}); err != nil {
		t.Fatal(err)
	}
	Info("不该出现")
	if buf.Len() != 0 {
		t.Fatalf("error 级别下 info 不该输出: %s", buf.String())
	}
	SetLevel(slog.LevelDebug)
	Info("现在该出现了")
	if !strings.Contains(buf.String(), "现在该出现了") {
		t.Error("SetLevel 未生效")
	}
	if !IsDebug() {
		t.Error("IsDebug 应为 true")
	}
}

// 配错格式/级别必须**报错**，不能静默用默认值 ——
// 否则运维写了 MYNET_LOG_FORMAT=Json（大写）以为切了结构化，实际还是 text，
// 日志系统解析失败，而现场看不出任何异常。
func TestInvalidConfigRejected(t *testing.T) {
	if err := Init(Config{Format: "yaml"}); err == nil {
		t.Error("未知格式应报错")
	}
	if err := Init(Config{Level: "trace"}); err == nil {
		t.Error("未知级别应报错")
	}
	// 大小写与空格要容忍（环境变量里很容易带上）
	if err := Init(Config{Format: " JSON ", Level: " Warn "}); err != nil {
		t.Errorf("大小写/空格应被容忍，却报错: %v", err)
	}
}

// 未调用 Init 时包级函数也必须能用 —— 日志系统自己不该成为
// 「忘了初始化就 panic」的故障点。
func TestUsableWithoutInit(t *testing.T) {
	// 注意：这条测试依赖包级默认值，所以放在会改全局的测试之后也能跑
	// （Init 只是替换 current，不会置 nil）。
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("未 Init 就调用日志函数 panic 了: %v", r)
		}
	}()
	Info("没有 Init 也能记日志")
}

// ParseLevel 的映射要覆盖常见写法。
func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"": slog.LevelInfo, "info": slog.LevelInfo, "INFO": slog.LevelInfo,
		"debug": slog.LevelDebug, "warn": slog.LevelWarn, "warning": slog.LevelWarn,
		"error": slog.LevelError,
	}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil {
			t.Errorf("ParseLevel(%q) 报错: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseLevel(%q) = %v，期望 %v", in, got, want)
		}
	}
	if _, err := ParseLevel("verbose"); err == nil {
		t.Error("ParseLevel(\"verbose\") 应报错")
	}
}

// 结构化参数传错（奇数个、key 不是 string）时 slog 会补 !BADARG 而不是 panic。
// 这条锁住「不会崩」—— 日志把进程搞崩是最糟的失败方式。
func TestBadArgsDoNotPanic(t *testing.T) {
	var buf bytes.Buffer
	if err := Init(Config{Format: "json", Output: &buf}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("参数传错导致 panic: %v", r)
		}
	}()
	Info("奇数个参数", "only_key")
	Info("key 不是 string", 42, "value")
	if buf.Len() == 0 {
		t.Error("即使参数有问题，日志也该输出（带 !BADARG 标记）")
	}
}
