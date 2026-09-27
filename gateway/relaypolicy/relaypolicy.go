// Package relaypolicy 是纯路径白名单（B3：不得含鉴权）。
//
// IsAllowedWith 先查 deny 再查 allow（隐性知识，改白名单必须两动作同时做）。
// 含 % 的路径一律拒（防 %2f 绕过）。
package relaypolicy

import "strings"

// AllowedPathPrefixes 允许中继的 back 路径前缀（含节点自身通道 + mynet_gui 面）。
//
// ⚠️ 改白名单必须**同时**看 deniedFragments（IsAllowedWith 先 deny 后 allow）。
//
// 起初只有 C-8 会话 5 条；mynet_gui 收进本机 webui 后，控制台要调
// nodes/routes/services/social/ippools/tenants/user 等一大片 —— 不放开
// 就是一串 403「path 不在中继白名单内」（实测）。这里按**业务前缀**放开，
// 不是按单条路径枚举：back 自己还有鉴权与租户隔离，gateway 只挡路径。
var AllowedPathPrefixes = []string{
	// 会话与账号（C-8 原 5 条 + gui 的注册/改密/找回）
	"/api/v1/auth/",
	// 节点面
	"/api/v1/nodes",
	"/api/v1/routes",
	"/api/v1/route/",
	"/api/v1/monitor",
	"/api/v1/heartbeat",
	// gui 控制台：IP 池 / 服务 / 社交 / 租户 / 用户
	"/api/v1/ippools",
	"/api/v1/ip-pools",
	"/api/v1/services",
	"/api/v1/social",
	"/api/v1/tenants",
	"/api/v1/user",
	"/api/v1/users",
	"/api/v1/dashboard",
	"/api/v1/config",
	"/api/v1/system",
	// 通知与头像（gui 主布局启动即调，缺了就一片 403）
	"/api/v1/notifications",
	"/api/v1/files",
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
