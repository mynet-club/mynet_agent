package localapi

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// /gui/ —— mynet_gui（后端的前端控制台）收进本机 webui。
//
// 构建时把 API 基址设成 `/gui`，于是 axios 的 baseURL 是 `/gui/api/v1`，
// 资源前缀是 `/gui/assets`。本面做两件事：
//
//	/gui/api/v1/*  → 剥掉 /gui 前缀，走 relay（mynetd → gateway → 后端）
//	/gui/*         → 静态文件（Quasar SPA）
//
// 为什么不 iframe 生产站：那套界面的数据要经后端的鉴权与租户，
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
	// Release packages carry the same-origin GUI alongside the native WebUI.
	// Prefer the compiled copy so an older on-disk GUI cannot shadow an update.
	if embedded, err := fs.Sub(webUIFS(), "gui"); err == nil {
		if _, err := fs.Stat(embedded, "index.html"); err == nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path = p
			name := strings.TrimPrefix(filepath.Clean(p), "/")
			if st, err := fs.Stat(embedded, name); err != nil || st.IsDir() {
				r2.URL.Path = "/index.html"
			}
			if r2.URL.Path == "/index.html" {
				body, err := fs.ReadFile(embedded, "index.html")
				if err != nil {
					http.Error(w, "GUI unavailable", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				if r.Method != http.MethodHead {
					_, _ = w.Write(body)
				}
				return
			}
			http.FileServer(http.FS(embedded)).ServeHTTP(w, r2)
			return
		}
	}
	if root == "" {
		http.Error(w, "gui 未安装（找不到 mynet_gui/dist/spa）。设 MYNET_GUI_DIR 或把产物放到 <home>/gui", 404)
		return
	}

	// SPA 用 history 路由：非文件请求一律回 index.html
	//
	// ⚠️ 必须把 URL.Path 改写成剥掉 /gui 之后的路径再交给 FileServer ——
	// 直接 fs.ServeHTTP(w, r) 会去找 <root>/gui/assets/…，而产物在
	// <root>/assets/…。症状是 index.html 200、JS/CSS 全 404（实测）。
	clean := filepath.Clean(p)
	if clean == "/" || !fileExists(filepath.Join(root, clean)) {
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
		return
	}
	fs := http.FileServer(http.Dir(root))
	r2 := r.Clone(r.Context())
	r2.URL.Path = clean
	fs.ServeHTTP(w, r2)
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
