package utils

// 目录与权限常量。VPN 类型字符串以 shared/domain 为唯一来源。
const (
	AppName        = "mynet"
	AppVersion     = "dev" // 构建时由 -ldflags 覆盖 Version
	AppDescription = "MyNet VPN Node Management Client"
)

// Configuration constants
const (
	ConfigDirName  = ".mynet"
	ConfigFileName = "config.json"
	ConfigDirPerm  = 0o700
	ConfigFilePerm = 0o600
	LogFilePerm    = 0o644
	PidFilePerm    = 0o644
)

// Node status
const (
	NodeStatusOnline  = 1
	NodeStatusOffline = 0
	NodeStatusUnknown = -1

	NodeStatusOnlineStr  = "online"
	NodeStatusOfflineStr = "offline"
	NodeStatusUnknownStr = "unknown"
)

// Directory structure constants（相对 MYNET_HOME）
const (
	DirBin       = "bin"
	DirConf      = "conf"
	DirVar       = "var"
	DirVPN       = "driver"
	DirGNB       = "driver/gnb"
	DirWireGuard = "driver/wireguard"
	DirLogs      = "var/logs"
	DirToken     = "var/token"
	DirPID       = "var/pid"
	DirCache     = "pkg/cache"
)

// Log constants
const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"

	LogFormatJSON = "json"
	LogFormatText = "text"
)

// Build constants（构建时覆盖）
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
	GitBranch = "unknown"
)
