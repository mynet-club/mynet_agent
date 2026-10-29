package localapi

// daemon/quit —— 托盘「退出应用」用：停隧道 + 让本机服务退出。
// 产品语义（2026-10-28 用户裁定）：普通用户点「退出」= 整个应用退出，
// 不留在后台跑隧道/守护。

import (
	"net/http"
)

func (s *Server) handleDaemonQuit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "POST only"})
		return
	}
	if s.DaemonQuit == nil {
		writeJSON(w, 503, map[string]any{"success": false, "message": "daemon quit 未装配"})
		return
	}
	// 用户明确退出应用：清「想要隧道」意图，避免服务再被拉起时自动连上。
	markTunnelWant(false)
	if err := s.DaemonQuit(); err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "message": "正在退出本机服务"})
}
