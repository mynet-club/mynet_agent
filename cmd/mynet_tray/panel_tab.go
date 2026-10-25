package main

import (
	"net/url"
	"sync"
)

// 面板所有 hash 页面共享一个标签页；不同本机 API 实例仍分别打开。
func panelTabKey(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "/ui/" {
		return "", false
	}
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	u.ForceQuery = false
	return u.String(), true
}

// 快速重复点击也串行查找/创建，避免同时发现不存在后各新建一个标签页。
var browserOpenMu sync.Mutex
