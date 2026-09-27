package logger

import (
	"fmt"
	"sync"
	"time"
)

// Ring —— 进程内最近 N 条日志，给本机 WebUI「诊断 → 日志」用。
//
// 为什么还要一份内存环：/var/log 只有 launchd 重定向的 stderr，
// 而 localapi 请求、apply 路由/防火墙、驱动启停这些**事件**原先只打 stderr
// 或干脆不打 —— UI 上「降级了却看不到原因」就是这么来的。
// 环形缓冲无文件依赖，mynetd 一起来就能给 UI 喂真实事件。
//
// 并发：写日志的路径多（HTTP 中间件、心跳、transport），读是 UI 轮询。
// 用互斥锁即可 —— 容量默认 400，锁竞争可忽略。
type Ring struct {
	mu   sync.RWMutex
	buf  []Entry
	max  int
	next int // 环形写指针；len(buf)==max 后覆盖最旧
	full bool
}

// Entry 一条已脱敏前的结构化日志。
type Entry struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Comp  string    `json:"comp,omitempty"`
	Msg   string    `json:"msg"`
	Attrs string    `json:"attrs,omitempty"` // key=value 空格拼接
}

const DefaultRingSize = 400

// NewRing 创建环。max<=0 时用 DefaultRingSize。
func NewRing(max int) *Ring {
	if max <= 0 {
		max = DefaultRingSize
	}
	return &Ring{buf: make([]Entry, max), max: max}
}

// Add 追加一条。
func (r *Ring) Add(e Entry) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = e
	r.next = (r.next + 1) % r.max
	if r.next == 0 {
		r.full = true
	}
}

// Recent 最近 n 条（旧→新）。n<=0 返回全部。
func (r *Ring) Recent(n int) []Entry {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []Entry
	if r.full {
		all = append(all, r.buf[r.next:]...)
		all = append(all, r.buf[:r.next]...)
	} else {
		all = append(all, r.buf[:r.next]...)
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	out := make([]Entry, len(all))
	copy(out, all)
	return out
}

// Len 当前条数。
func (r *Ring) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.full {
		return r.max
	}
	return r.next
}

// ── 全局环 + 组件 logger ───────────────────────────────────────

var (
	ringMu     sync.RWMutex
	globalRing = NewRing(DefaultRingSize)
)

// SetRing 替换全局环（测试/自定义容量）。
func SetRing(r *Ring) {
	ringMu.Lock()
	globalRing = r
	ringMu.Unlock()
}

// GlobalRing 当前全局环。
func GlobalRing() *Ring {
	ringMu.RLock()
	defer ringMu.RUnlock()
	return globalRing
}

// Comp 返回带 component 字段的 logger。语义：mynetd / localapi / transport /
// driver / apply / gateway / cli。
func Comp(name string) *CompLogger {
	return &CompLogger{comp: name}
}

// CompLogger 组件 logger：包一层，保证每条都进 Ring。
type CompLogger struct {
	comp string
}

func (c *CompLogger) Debug(msg string, args ...any) { c.at("DEBUG", msg, args) }
func (c *CompLogger) Info(msg string, args ...any)  { c.at("INFO", msg, args) }
func (c *CompLogger) Warn(msg string, args ...any)  { c.at("WARN", msg, args) }
func (c *CompLogger) Error(msg string, args ...any) { c.at("ERROR", msg, args) }

func (c *CompLogger) at(level, msg string, args []any) {
	attrs := formatAttrs(args)
	// 1) 环形缓冲（给 UI）
	GlobalRing().Add(Entry{
		Time:  time.Now(),
		Level: level,
		Comp:  c.comp,
		Msg:   msg,
		Attrs: attrs,
	})
	// 2) 标准 logger（stderr / 文件）
	kv := append([]any{"comp", c.comp}, args...)
	switch level {
	case "DEBUG":
		Debug(msg, kv...)
	case "WARN":
		Warn(msg, kv...)
	case "ERROR":
		Error(msg, kv...)
	default:
		Info(msg, kv...)
	}
}

// Event 便捷入口：组件 + 级别 + 消息。等价 logger.Comp(c).Info(...)。
func Event(comp, level, msg string, args ...any) {
	Comp(comp).at(level, msg, args)
}

func formatAttrs(args []any) string {
	if len(args) == 0 {
		return ""
	}
	s := ""
	for i := 0; i < len(args); i += 2 {
		if i > 0 {
			s += " "
		}
		k := "?"
		if i < len(args) {
			k = fmt.Sprint(args[i])
		}
		v := ""
		if i+1 < len(args) {
			v = fmt.Sprint(args[i+1])
		}
		s += k + "=" + v
	}
	return s
}
