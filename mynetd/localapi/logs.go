package localapi

import (
	"bufio"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/logger"
)

// handleLogs —— 本机运行日志（诊断页「日志」）。
//
// 两个来源：
//  1. **内存环** logger.GlobalRing —— localapi 请求、apply、驱动启停等事件
//  2. **文件尾部** /var/log/mynet/{mynetd,gnb}.{log,err} —— 进程 stderr / gnb 输出
//
// 都过 redactSecrets 脱敏后才出机。
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]any{"success": false, "message": "GET only"})
		return
	}
	lines := 80
	if q := r.URL.Query().Get("lines"); q != "" {
		var n int
		for _, c := range q {
			if c < '0' || c > '9' {
				n = 0
				break
			}
			n = n*10 + int(c-'0')
		}
		if n > 0 && n <= 400 {
			lines = n
		}
	}

	// ① 内存事件环
	evs := logger.GlobalRing().Recent(lines)
	type evJSON struct {
		Time  string `json:"time"`
		Level string `json:"level"`
		Comp  string `json:"comp,omitempty"`
		Msg   string `json:"msg"`
		Attrs string `json:"attrs,omitempty"`
	}
	events := make([]evJSON, 0, len(evs))
	for _, e := range evs {
		events = append(events, evJSON{
			Time:  e.Time.Format("15:04:05"),
			Level: e.Level,
			Comp:  e.Comp,
			Msg:   redactSecrets(e.Msg),
			Attrs: redactSecrets(e.Attrs),
		})
	}

	// ② 文件尾部
	type src struct {
		Name string `json:"name"`
		Text string `json:"text"`
	}
	out := []src{}
	candidates := []struct{ name, path string }{
		{"mynetd.err", "/var/log/mynet/mynetd.err"},
		{"mynetd.log", "/var/log/mynet/mynetd.log"},
		{"gnb.log", "/var/log/mynet-gnb.log"},
		{"gnb.err", "/var/log/mynet-gnb.err"},
	}
	for _, c := range candidates {
		text := tailFile(c.path, lines)
		if text == "" {
			continue
		}
		out = append(out, src{Name: c.name, Text: redactSecrets(text)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	writeJSON(w, 200, map[string]any{
		"success": true,
		"data":    map[string]any{"events": events, "files": out},
	})
}

// tailFile 读文件末尾最多 n 行；读不到返回空串。
func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	// 简单环形缓冲：日志文件可控（几十 KB～几 MB），整读再截尾足够
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	ring := make([]string, 0, n)
	for sc.Scan() {
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, sc.Text())
	}
	if len(ring) == 0 {
		return ""
	}
	return strings.Join(ring, "\n")
}
