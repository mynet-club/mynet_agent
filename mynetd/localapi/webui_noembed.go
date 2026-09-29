//go:build !embed && !dev

package localapi

// hasWebUI OpenWrt/瘦构建无 embed（LuCI 托管 UI）
const hasWebUI = false
