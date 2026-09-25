package localapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// /gui/ —— mynet_gui（back 的前端控制台）收进本机 webui。
//
// 构建时把 API 基址设成 `/gui`，于是 axios 的 baseURL 是 `/gui/api/v1`，
// 资源前缀是 `/gui/assets`。本面做两件事：
//
//	/gui/api/v1/*  → 剥掉 /gui 前缀，走 relay（mynetd → gateway → back）
//	/gui/*         → 静态文件（Quasar SPA）
//
// 为什么不 iframe 生产站：那套界面的数据要经 back 的鉴权与租户，
// 嵌进本机后浏览器仍只谈 127.0.0.1，出网一律走 relay —— 与
// 「信任边界止于本机」是同一条原则。
func (s *Server) handleGUI(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/gui")
	if p == "" {
		p = "/"
	}

	// API：/gui/api/v1/nodes → relay path=/api/v1/nodes
	if strings.HasPrefix(p, "/api/") {
		r2 := r.Clone(r.Context())
		q := r.URL.Query()
		q.Set("path", p)
		r2.URL.RawQuery = q.Encode()
		s.handleRelay(w, r2)
		return
	}

	root := guiRoot()
	if root == "" {
		http.Error(w, "gui 未安装（找不到 mynet_gui/dist/spa）。设 MYNET_GUI_DIR 或把产物放到 <home>/gui", 404)
		return
	}

	// SPA 用 history 路由：非文件请求一律回 index.html
	fs := http.FileServer(http.Dir(root))
	clean := filepath.Clean(p)
	if clean == "/" || !fileExists(filepath.Join(root, clean)) {
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
		return
	}
	fs.ServeHTTP(w, r)
}

// guiRoot 找 SPA 产物目录。优先 MYNET_GUI_DIR，再看 <MYNET_HOME>/gui，
// 最后试工作区里的 mynet_gui/dist/spa（开发态）。
func guiRoot() string {
	for _, d := range []string{
		os.Getenv("MYNET_GUI_DIR"),
		filepath.Join(guiHome(), "gui"),
		filepath.Join(guiHome(), "gui", "spa"),
		"../mynet_gui/dist/spa",
		"/Users/arthur/Devel/WorkSpaceAI/mynet-club/mynet_gui/dist/spa",
	} {
		if d == "" {
			continue
		}
		if fileExists(filepath.Join(d, "index.html")) {
			return d
		}
	}
	return ""
}

func guiHome() string {
	if h := os.Getenv("MYNET_HOME"); h != "" {
		return h
	}
	return "."
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
