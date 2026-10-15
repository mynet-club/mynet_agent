//go:build embed

package localapi

import (
	"embed"
	"io/fs"
)

//go:embed all:webui
var webuiEmbed embed.FS

const allowDiskGUI = false

func webUIFS() fs.FS {
	sub, _ := fs.Sub(webuiEmbed, "webui")
	return sub
}
