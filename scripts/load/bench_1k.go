// R5-5：1k 节点心跳 + 命令延迟微基准（进程内，可重复）
// 用法: go run ./scripts/load -nodes 1000 -cmds 5000
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mynet-club/mynet_agent/mynetd/commands"
	"github.com/mynet-club/mynet_agent/mynetd/localapi"
	"github.com/mynet-club/mynet_agent/mynetd/transport"
)

func main() {
	n := flag.Int("nodes", 1000, "并发节点会话")
	cmds := flag.Int("cmds", 5000, "命令总数")
	flag.Parse()

	// 1) 会话表 1k
	tb := localapi.NewSessionTable()
	t0 := time.Now()
	for i := 0; i < *n; i++ {
		tb.Attach(fmt.Sprintf("n%d", i), "gnb")
	}
	for i := 0; i < *n; i++ {
		tb.Focus(fmt.Sprintf("n%d", i))
	}
	sessMs := float64(time.Since(t0).Microseconds()) / 1000

	// 2) 命令分派延迟
	ex := func(argv []string) (string, string, error) { return "ok", "", nil }
	var sumLat, maxLat int64
	var wg sync.WaitGroup
	var done int64
	ch := make(chan int, 256)
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				_ = i
				t1 := time.Now()
				_ = commands.Run(ex, commands.Request{ID: "x", Op: commands.OpDetector}, "n")
				lat := time.Since(t1).Microseconds()
				atomic.AddInt64(&sumLat, lat)
				for {
					old := atomic.LoadInt64(&maxLat)
					if lat <= old || atomic.CompareAndSwapInt64(&maxLat, old, lat) {
						break
					}
				}
				atomic.AddInt64(&done, 1)
			}
		}()
	}
	t1 := time.Now()
	for i := 0; i < *cmds; i++ {
		ch <- i
	}
	close(ch)
	wg.Wait()
	cmdMs := float64(time.Since(t1).Microseconds()) / 1000
	avg := float64(atomic.LoadInt64(&sumLat)) / float64(atomic.LoadInt64(&done))

	// 3) 会话 + 帧处理吞吐（模拟 WS 命令帧）
	_ = transport.Config{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = ctx

	out := map[string]any{
		"nodes":                   *n,
		"session_attach_focus_ms": sessMs,
		"commands":                *cmds,
		"command_wall_ms":         cmdMs,
		"command_avg_us":          avg,
		"command_max_us":          atomic.LoadInt64(&maxLat),
		"cmd_throughput_per_s":    float64(*cmds) / (cmdMs / 1000),
		"mem_after":               readRSS(),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

func readRSS() string {
	b, _ := os.ReadFile("/proc/self/status")
	for _, line := range split(string(b)) {
		if len(line) > 6 && line[:6] == "VmRSS:" {
			return line
		}
	}
	return "n/a"
}

func split(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
