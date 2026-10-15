//go:build dev && !embed

package localapi

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing/fstest"
)

// hasWebUI 开发目录模式：从 MYNET_WEBUI_DIR 读 webui，改前端不必重编 mynetd。
//
// ⚠️ 仅限开发（`-tags dev`）。**发布构建不得带 dev**：
// pack/release 一律 `-tags embed`，产品包里没有本文件的逻辑。
const hasWebUI = true

// webUIFS 开发目录 FS。MYNET_WEBUI_DIR 未设时回落到源码树 webui/。
const allowDiskGUI = true

func webUIFS() fs.FS {
	dir := os.Getenv("MYNET_WEBUI_DIR")
	if dir == "" {
		// 常见开发路径：mynetd/localapi/webui
		for _, c := range []string{
			"mynetd/localapi/webui",
			"../webui",
			"./webui",
		} {
			if st, err := os.Stat(c); err == nil && st.IsDir() {
				dir = c
				break
			}
		}
	}
	if dir == "" {
		return fstest.MapFS{}
	}
	abs, err := filepath.Abs(dir)
	if err == nil {
		dir = abs
	}
	devWebUIDir = dir
	return os.DirFS(dir)
}

var devWebUIDir string

// DevWebUIDir 供日志打印；非 dev 构建为空。
func DevWebUIDir() string { return devWebUIDir }
