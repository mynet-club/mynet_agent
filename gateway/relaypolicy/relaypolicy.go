// Package relaypolicy 是纯路径白名单（B3：不得含鉴权）。
//
// IsAllowedWith 先查 deny 再查 allow（隐性知识，改白名单必须两动作同时做）。
// 含 % 的路径一律拒（防 %2f 绕过）。
package relaypolicy

import "strings"

// AllowedPathPrefixes 允许中继的 back 路径前缀（含节点自身通道）
var AllowedPathPrefixes = []string{
	// C-8 会话 5 条
	"/api/v1/auth/login",
	"/api/v1/auth/refresh",
	"/api/v1/auth/logout",
	"/api/v1/auth/verify",
	"/api/v1/auth/me",
	"/api/v1/nodes",
	"/api/v1/routes",
	"/api/v1/monitor",
	"/api/v1/heartbeat",
}

// deniedFragments 即便前缀命中也必须拒绝。
// C-8：原 `/auth`·`/login`·`/logout` 过宽，会误伤 auth/{login,logout}——已删；
// 精确放行会话 5 条见 AllowedPathPrefixes。
var deniedFragments = []string{
	"/auth/admin",
	"/admin",
}

// IsAllowedWith 先 deny 后 allow；含 % 一律拒。
func IsAllowedWith(path string, prefixes []string) bool {
	if strings.Contains(path, "%") {
		return false
	}
	for _, bad := range deniedFragments {
		if strings.Contains(path, bad) {
			return false
		}
	}
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// IsAllowed 使用默认白名单
func IsAllowed(path string) bool {
	return IsAllowedWith(path, AllowedPathPrefixes)
}
