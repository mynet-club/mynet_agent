package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// NodeTypeGNB / NodeTypeWireGuard —— 字符串形态，供按字符串比较的调用方使用。
// 唯一来源是 shared/domain 的 VPNDriverType。
const (
	NodeTypeGNB       = string(domain.DriverTypeGNB)
	NodeTypeWireGuard = string(domain.DriverTypeWireGuard)
)

// GetDefaultPaths 返回默认应用路径布局
func GetDefaultPaths() *ConfigPaths {
	root := GetProjectRoot()
	return &ConfigPaths{
		Root:      root,
		Bin:       filepath.Join(root, DirBin),
		Conf:      filepath.Join(root, DirConf),
		Var:       filepath.Join(root, DirVar),
		VPN:       filepath.Join(root, DirVPN),
		GNB:       filepath.Join(root, DirGNB),
		WireGuard: filepath.Join(root, DirWireGuard),
		Logs:      filepath.Join(root, DirLogs),
		Token:     filepath.Join(root, DirToken),
		PID:       filepath.Join(root, DirPID),
		Cache:     filepath.Join(root, DirCache),
	}
}

// GetProjectRoot 向上找 go.mod；找不到则返回当前目录
func GetProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

// NodeStatusToString 数字状态 → 字符串
func NodeStatusToString(status int) string {
	switch status {
	case NodeStatusOnline:
		return NodeStatusOnlineStr
	case NodeStatusOffline:
		return NodeStatusOfflineStr
	default:
		return NodeStatusUnknownStr
	}
}

// NodeStatusFromString 字符串状态 → 数字
func NodeStatusFromString(status string) int {
	switch strings.ToLower(status) {
	case NodeStatusOnlineStr:
		return NodeStatusOnline
	case NodeStatusOfflineStr:
		return NodeStatusOffline
	default:
		return NodeStatusUnknown
	}
}

// FormatDuration 人类可读时长
func FormatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.0f秒", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%.0f分钟", d.Minutes())
	case d < 24*time.Hour:
		return fmt.Sprintf("%.1f小时", d.Hours())
	default:
		days := int(d.Hours() / 24)
		hours := int(d.Hours()) % 24
		return fmt.Sprintf("%d天%d小时", days, hours)
	}
}

// FormatBytes 人类可读字节数
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// GetPlatformInfo 平台标识
func GetPlatformInfo() string {
	return fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
}

// GetBuildInfo 构建信息
func GetBuildInfo() *BuildInfo {
	return &BuildInfo{
		Version:   Version,
		BuildTime: BuildTime,
		GitCommit: GitCommit,
		GitBranch: GitBranch,
		GoVersion: runtime.Version(),
		Platform:  GetPlatformInfo(),
	}
}

// IsValidNodeType 是否合法节点/驱动类型
func IsValidNodeType(nodeType string) bool {
	return nodeType == NodeTypeGNB || nodeType == NodeTypeWireGuard
}

// IsValidLogLevel 是否合法日志级别
func IsValidLogLevel(level string) bool {
	switch level {
	case LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError:
		return true
	default:
		return false
	}
}

// EnsureDirectories 创建应用目录树
func EnsureDirectories(paths *ConfigPaths) error {
	dirs := []string{
		paths.Root, paths.Bin, paths.Conf, paths.Var,
		paths.VPN, paths.GNB, paths.WireGuard,
		paths.Logs, paths.Token, paths.PID, paths.Cache,
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}
