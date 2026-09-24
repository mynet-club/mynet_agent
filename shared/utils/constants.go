package utils

// 目录与权限常量。VPN 类型字符串以 shared/domain 为唯一来源。
const (
	AppName        = "mynet"
	AppDescription = "MyNet VPN Node Management Client"
)

// AppVersion 构建时由 -ldflags -X 注入（见 Makefile 的 GO_LDFLAGS）。
//
// ⚠️ 必须是 **var**，不能是 const：`-X` 只能覆盖变量，对常量无效且**不报错**
// —— 放在上面那个 const 块里时，ldflags 静默失效，产物版本恒输出 "dev"。
// 原注释写着「构建时由 -ldflags 覆盖」但它是 const，所以从未生效过。
var AppVersion = "dev"

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
