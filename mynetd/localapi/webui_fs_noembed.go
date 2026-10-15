//go:build !embed && !dev

package localapi

import (
	"io/fs"
	"testing/fstest"
)

// OpenWrt/瘦构建无 WebUI 产物；返回空 FS
const allowDiskGUI = true

func webUIFS() fs.FS { return fstest.MapFS{} }
