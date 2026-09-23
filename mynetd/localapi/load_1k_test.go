package localapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// R5-5：1k 节点会话 + 命令帧 HTTP 吞吐
func TestLoad1kNodesAndCommands(t *testing.T) {
	s := newTestServer(t)
	s.Ops = func(argv []string) (any, error) {
		return map[string]any{"ok": true}, nil
	}
	h := s.withSecurity(s.mux)

	// 1k 会话
	for i := 0; i < 1000; i++ {
		s.sessions.Attach(fmt.Sprintf("n%d", i), "gnb")
	}
	if s.sessions.ParallelCount() != 1000 {
		t.Fatalf("parallel=%d", s.sessions.ParallelCount())
	}

	const N = 3000
	var wg sync.WaitGroup
	t0 := time.Now()
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < N/32; i++ {
				body, _ := json.Marshal(map[string]any{"argv": []string{"metrics", "--json"}})
				req := httptest.NewRequest("POST", "/api/v1/command", bytes.NewReader(body))
				req.Host = "127.0.0.1:8096"
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != 200 {
					t.Errorf("cmd %d", rec.Code)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	el := time.Since(t0)
	t.Logf("1k sessions + %d cmds in %s (%.0f/s)", N, el, float64(N)/el.Seconds())
	if el > 5*time.Second {
		t.Fatalf("too slow: %s", el)
	}
}
