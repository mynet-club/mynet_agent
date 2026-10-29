package localapi

import (
	"net/http"
	"runtime"
)

func runtimePlatform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// GET /api/v1/experience/capabilities —— 桌面/网关能力声明（冻结形状见 webui-spa-page-map.md §6）。
// 前端只信这里声明的 features，不用操作系统名猜高级页。
// 形态来源执行器 `config form --json`（B2：localapi 不 import shared/config）。
func (s *Server) handleExperienceCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "GET only"})
		return
	}
	form := "pc"
	choices := []string{"pc"}
	if data, err := s.opsJSON([]string{"config", "form", "--json"}); err == nil {
		if m, _ := data["mode"].(string); m != "" {
			form = m
		}
		if arr, ok := data["choices"].([]any); ok && len(arr) > 0 {
			choices = choices[:0]
			for _, c := range arr {
				if str, _ := c.(string); str != "" {
					choices = append(choices, str)
				}
			}
		}
	}
	platform := runtimePlatform()
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data": map[string]any{
			"form":     form,
			"platform": platform,
			"choices":  choices,
			"features": featuresForForm(form),
		},
	})
}

// featuresForForm 由形态声明能力；与 webui-spa-page-map.md §6 一致。
// 不按 OS 名分叉 —— OpenWrt/网关只通过 form=gw 暴露高级面。
func featuresForForm(form string) map[string]any {
	if form == "gw" {
		return map[string]any{
			"desktop_acceleration": false,
			"dns_hook":             false,
			"proxy_policy":         true,
			"acl":                  true,
			"routes_edit":          true,
			"plugins":              true,
		}
	}
	return map[string]any{
		"desktop_acceleration": true,
		"dns_hook":             true,
		"proxy_policy":         false,
		"acl":                  false,
		"routes_edit":          false,
		"plugins":              false,
	}
}
