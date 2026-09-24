// Package logger 是标准库 log/slog 的薄封装：统一级别、格式与输出目标。
//
// # 为什么重写（2026-09-24，V4-4）
//
// 原实现有 336 行，但**全仓零消费者** —— 各处日志实际都是裸的
// `fmt.Fprintf(os.Stderr, …)`。它没被用起来不是偶然，是设计不对路：
//
//   - **只写文件**：`Init` 里明确写着 "Always write only to file; avoid console
//     output for interactive app"。而 mynetd / mynet_gateway 是 **systemd 服务**，
//     它们的日志该走 stderr 进 journald（`journalctl -u mynet_gateway`）。
//     写文件反而要额外配 logrotate，且 journalctl 里什么都看不到。
//   - **纯文本 + printf 风格**：`Info("node %s connected", id)` 出来的是一句话，
//     机器没法解析。V4-4 的判据恰恰是「关键路径日志可被机器解析」。
//   - **`SetLevel` 改的是无锁字段**，并发调用是数据竞争（`go test -race` 会报）。
//
// 现在内部就是 slog：text（logfmt，`key=value`）与 json 两种 handler，
// 默认输出 stderr，级别用 `slog.LevelVar` 支持并发安全地动态调整。
//
// # 调用方式
//
//	logger.Info("节点接入", "node", nodeID, "conns", n)
//	// text: time=2026-09-24T14:03:19Z level=INFO msg=节点接入 node=1137… conns=1
//	// json: {"time":"…","level":"INFO","msg":"节点接入","node":"1137…","conns":1}
//
// 参数是 **key/value 交替**（slog 的约定），不是 printf 的格式串。
// 传错（奇数个、或 key 不是 string）slog 会自己补 `!BADARG`，不会 panic。
//
// # 范围
//
// 只接**常驻进程**（mynetd / mynet_gateway）的关键路径。`mynet` 是 CLI 执行器，
// 它的输出是给人看的（`mynet status`、`mynet metrics`），保持人类可读，
// 不套结构化日志 —— 把 CLI 输出改成 JSON 只会让它更难用。
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Config 装配参数。零值即可用（info + text + stderr）。
type Config struct {
	// Level: debug | info | warn | error（大小写不敏感）。空 = info。
	Level string
	// Format: text | json（大小写不敏感）。空 = text。
	// text 是 logfmt（key=value），同样可被机器解析，但人读着舒服；
	// json 给日志系统用。
	Format string
	// Dir 非空时**额外**写一份到 Dir/mynet-YYYY-MM-DD.log（保留原实现的按日分文件）。
	// 与 Output 是「都要」而不是二选一 —— stderr 给 journald，文件给没有
	// journald 的环境（比如 OpenWrt 上手动跑）。
	Dir string
	// Output 默认 os.Stderr。测试可以塞 bytes.Buffer。
	Output io.Writer
}

var (
	mu       sync.RWMutex
	current  = slog.New(slog.NewTextHandler(os.Stderr, nil))
	levelVar = new(slog.LevelVar)

	// 文件写入侧（Dir 非空时才有），Close 时要收尾
	logFile *os.File
)

// Init 按配置装配全局 logger。可以多次调用（后一次覆盖前一次）。
//
// ⚠️ 未调用 Init 时包级函数也能用 —— 默认是 info + text + stderr。
// 这是刻意的：日志系统自己不该成为「忘了初始化就 panic」的故障点。
func Init(cfg Config) error {
	lvl, err := ParseLevel(cfg.Level)
	if err != nil {
		return err
	}
	levelVar.Set(lvl)

	out := cfg.Output
	if out == nil {
		out = os.Stderr
	}

	if cfg.Dir != "" {
		if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
			return fmt.Errorf("创建日志目录 %s: %w", cfg.Dir, err)
		}
		name := filepath.Join(cfg.Dir, "mynet-"+time.Now().Format("2006-01-02")+".log")
		f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("打开日志文件 %s: %w", name, err)
		}
		mu.Lock()
		if logFile != nil {
			_ = logFile.Close()
		}
		logFile = f
		mu.Unlock()
		out = io.MultiWriter(out, f)
	}

	opts := &slog.HandlerOptions{
		Level: levelVar,
		// 带上源码位置：排查时「哪个文件哪一行」比什么都管用，
		// 而 slog 默认不给。
		AddSource: true,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// 时间戳统一 RFC3339（秒级足够；毫秒在文本模式里只是噪音，
			// 真要精确定位靠的是 source 而不是时间）
			if a.Key == slog.TimeKey && len(groups) == 0 {
				a.Value = slog.StringValue(a.Value.Time().Format(time.RFC3339))
			}
			// source 只留 file:line，完整路径太长且含本机目录
			if a.Key == slog.SourceKey && len(groups) == 0 {
				if src, ok := a.Value.Any().(*slog.Source); ok {
					return slog.String(slog.SourceKey, filepath.Base(src.File)+":"+itoa(src.Line))
				}
			}
			return a
		},
	}

	var h slog.Handler
	switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
	case "", "text", "logfmt":
		h = slog.NewTextHandler(out, opts)
	case "json":
		h = slog.NewJSONHandler(out, opts)
	default:
		return fmt.Errorf("logger: 未知格式 %q（只支持 text | json）", cfg.Format)
	}

	mu.Lock()
	current = slog.New(h)
	mu.Unlock()
	// 让 slog.Default() 也指向它，这样第三方库里的 slog 调用同样受控
	slog.SetDefault(current)
	return nil
}

// InitFromEnv 按环境变量装配，供常驻进程（mynetd / mynet_gateway）在 main 开头调用：
//
//	MYNET_LOG_LEVEL   debug|info|warn|error（默认 info）
//	MYNET_LOG_FORMAT  text|json（默认 text）
//	MYNET_LOG_DIR     非空则额外写一份 Dir/mynet-YYYY-MM-DD.log
//
// 用环境变量而不是 flag：这两个进程都由 systemd 拉起，
// `EnvironmentFile=/etc/mynet/*.env` 是最顺的注入方式（见
// mynet_agent/deploy/systemd/mynet_gateway.service）。
func InitFromEnv() error {
	return Init(Config{
		Level:  os.Getenv("MYNET_LOG_LEVEL"),
		Format: os.Getenv("MYNET_LOG_FORMAT"),
		Dir:    os.Getenv("MYNET_LOG_DIR"),
	})
}

// ParseLevel 解析级别字符串。空串按 info 处理（不是错误）。
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logger: 未知级别 %q（支持 debug|info|warn|error）", s)
	}
}

// SetLevel 动态调级别（并发安全 —— 用 slog.LevelVar，不是改裸字段）。
func SetLevel(lvl slog.Level) { levelVar.Set(lvl) }

// L 返回当前的 *slog.Logger，需要 With()/WithGroup() 时用。
func L() *slog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Debug/Info/Warn/Error 的 args 是 **key/value 交替**（slog 约定），不是 printf 参数。
//
// ⚠️ 四个都必须 `//go:noinline`：source 字段靠调用栈定位，而单行函数会被编译器
// 内联，内联后帧数与帧的归属都变了 —— 实测同一批里 Info / Warn / Error 的内联
// 决策**各不相同**，于是 source 有时指向本文件、有时一路跳到 testing.go。
// 禁止内联后帧数固定，callerPC 才能稳定拿到真正的调用方。
// 代价是每次日志多一次函数调用 —— 与日志本身的格式化/IO 开销相比可以忽略。

//go:noinline
func Debug(msg string, args ...any) { logAt(slog.LevelDebug, msg, args...) }

//go:noinline
func Info(msg string, args ...any) { logAt(slog.LevelInfo, msg, args...) }

//go:noinline
func Warn(msg string, args ...any) { logAt(slog.LevelWarn, msg, args...) }

//go:noinline
func Error(msg string, args ...any) { logAt(slog.LevelError, msg, args...) }

// logAt 自己构造 slog.Record 并**显式设 PC**，而不是调 l.Info()。
//
// 为什么绕这一下：slog 的 AddSource 是按「谁调用了 Logger.Info」算调用栈的，
// 而本包的 Info 又包了一层 —— 直接转发的话，**所有日志的 source 都会指向
// 本文件的那一行**，字段等于白给（排查时想知道的是「业务代码在哪打的」）。
// 自己建 Record 就能把 PC 指到真正的调用方。
//
// 这件事是测试抓出来的：断言 source 含 "logger_test.go:" 时，
// 实际拿到的是 "logger.go:189"。
func logAt(level slog.Level, msg string, args ...any) {
	l := L()
	ctx := context.Background()
	if !l.Enabled(ctx, level) {
		return
	}
	var r slog.Record
	r.Time = time.Now()
	r.Level = level
	r.Message = msg
	// 用 callerPC 而不是固定的 runtime.Caller(2)：单行包装函数会不会被内联
	// 由编译器决定，实测同一批里 Info 没被内联、Warn/Error 被内联，
	// 固定 skip 值不可能同时对两者都成立。
	if pc := callerPC(); pc != 0 {
		r.PC = pc
	}
	r.Add(args...)
	_ = l.Handler().Handle(ctx, r)
}

// callerPC 往上找第一个**不属于本包实现**的调用帧。
//
// 用 `runtime.CallersFrames` 而不是 `runtime.Caller(skip)`：后者对内联帧的
// 计数不可靠 —— 实测同一批单行包装函数里 Info / Warn / Error 的内联决策
// **各不相同**，任何固定 skip 值都不可能同时对三者成立（这个坑是测试抓出来的：
// 断言 source 指向调用方，实际一会儿是 logger.go、一会儿跳到 testing.go）。
// CallersFrames 会逐个展开内联帧，按文件路径判断才稳。
//
// ⚠️ `_test.go` 要放行：本包的测试文件路径同样含 `shared/logger/`，
// 一并跳过就会一路跳到 `testing.go` 去。
func callerPC() uintptr {
	var pcs [16]uintptr
	// skip=2：跳过 runtime.Callers 自身与 callerPC
	n := runtime.Callers(2, pcs[:])
	if n == 0 {
		return 0
	}
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if f.PC == 0 {
			return 0
		}
		slash := filepath.ToSlash(f.File)
		inPkg := strings.Contains(slash, "shared/logger/") && !strings.HasSuffix(slash, "_test.go")
		if !inPkg {
			return f.PC
		}
		if !more {
			return 0
		}
	}
}

// Close 收尾文件写入侧。stderr 不需要关。
func Close() {
	mu.Lock()
	f := logFile
	logFile = nil
	mu.Unlock()
	if f != nil {
		_ = f.Sync()
		_ = f.Close()
	}
}

// IsDebug 当前是否 debug 级（有些昂贵日志想按级别跳过时用）。
func IsDebug() bool { return levelVar.Level() <= slog.LevelDebug }

func itoa(n int) string { return fmt.Sprintf("%d", n) }
