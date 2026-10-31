package localapi

// tips/小助手知识库（tips-kb）本机透出：GUI/浏览器只打本机（127.0.0.1:8096），
// 不直连 ctl；拉取+校验+合并+缓存在 shared/tipskb（tips-kb-contract §3.3/§3.4/§3.5）。
//
//	GET /api/v1/kb/tips-kb.json → 有效集（合法 tips-kb.json 形状，application/json）
//
// 只读端点：无写面。缓存缺失（或形状已坏）时按需拉取一次；拉取失败回空 chunks 的
// 合法 manifest（§3.5：失败保留此前成功缓存、永不清空等重拉 —— 有缓存时根本不重拉）。

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/mynet-club/mynet_agent/shared/tipskb"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

func (s *Server) registerKBRoutes() {
	s.mux.HandleFunc("/api/v1/kb/tips-kb.json", getOnly(s.handleTipsKB))
}

func (s *Server) handleTipsKB(w http.ResponseWriter, r *http.Request) {
	dir := utils.LocalConfDir() // 与 proxy_domain_baseline.json 同位（conf/local）
	m, err := tipskb.Load(dir)
	if err != nil || m == nil {
		// 缓存缺失：按需拉一次；失败不落盘、回空载荷（§3.5）
		client := &http.Client{Timeout: 10 * time.Second}
		if rerr := tipskb.Refresh(r.Context(), client, tipskb.CTLBaseURL(), dir); rerr == nil {
			m, _ = tipskb.Load(dir)
		}
	}
	if m == nil {
		m = tipskb.EmptyManifest()
	}
	raw, jerr := json.Marshal(m)
	if jerr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": jerr.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
