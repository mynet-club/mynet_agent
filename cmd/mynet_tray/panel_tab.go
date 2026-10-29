package main

import (
	"net/url"
	"path"
	"strings"
	"sync"
)

// 面板所有 SPA 路由共享一个标签页；不同本机 API 实例仍分别打开。
// 只认本机面板前缀 /ui/（旧跳转）与 /gui/（SPA）；其它路径/主机不收。
func panelTabKey(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	p := path.Clean("/" + u.Path)
	if p != "/ui" && p != "/ui/" && p != "/gui" && !strings.HasPrefix(p, "/gui/") {
		return "", false
	}
	u.Path = "/gui/"
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	u.ForceQuery = false
	return u.String(), true
}

// 快速重复点击也串行查找/创建，避免同时发现不存在后各新建一个标签页。
var browserOpenMu sync.Mutex
