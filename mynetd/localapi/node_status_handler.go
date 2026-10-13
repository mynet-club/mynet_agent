package localapi

import (
	"encoding/json"
	"net/http"
)

// handleNodeStatus —— G3：状态计算在执行器（`mynet node-status --json`），
// localapi 只做 HTTP 壳 + 经 Ops 取 JSON。**不 import mynet 包**（B2）。
func (s *Server) handleNodeStatus(w http.ResponseWriter, r *http.Request) {
	if s.Ops == nil {
		s.Ops = defaultExecMynet
	}
	out, err := s.Ops([]string{"node-status", "--json"})
	if err != nil {
		payload := map[string]any{"success": false, "message": err.Error()}
		if m, ok := out.(map[string]any); ok {
			if v, ok := m["stderr"]; ok {
				payload["stderr"] = v
			}
		}
		writeJSON(w, 500, payload)
		return
	}
	// defaultExecMynet 信封：{stdout: "...json..."}
	var st map[string]any
	m, _ := out.(map[string]any)
	stdout, _ := m["stdout"].(string)
	if err := json.Unmarshal([]byte(stdout), &st); err != nil {
		writeJSON(w, 500, map[string]any{
			"success": false,
			"message": "执行器 status JSON 不可解析",
			"stderr":  m["stderr"],
		})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": st})
}
