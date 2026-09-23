package domain

// Domain entities - 纯粹的业务模型，无任何外部依赖

// User 用户领域模型
type User struct {
	ID     string
	Email  string
	Name   string
	ZoneID int64 // 关联的默认区域ID（后端登录响应提供）
}

// Zone 区域领域模型
type Zone struct {
	ID     int64
	Name   string
	Region string
}

// Node 节点领域模型
type Node struct {
	ID          int64
	NodeID      int64
	Name        string
	Type        string
	Status      NodeStatus
	ZoneID      int64
	UserID      int64
	VirtualIP   string
	ServerAddr  string
	LocalAddr   string
	Description string
	IsActive    bool
	CreatedAt   int64
	UpdatedAt   int64
	Config      *NodeConfig
}

// NodeStatus 节点状态
type NodeStatus string

const (
	NodeStatusOffline     NodeStatus = "offline"
	NodeStatusOnline      NodeStatus = "online"
	NodeStatusConnecting  NodeStatus = "connecting"
	NodeStatusStopped     NodeStatus = "stopped"
	NodeStatusRunning     NodeStatus = "running"
	NodeStatusActive      NodeStatus = "active"
	NodeStatusStarting    NodeStatus = "starting"
	NodeStatusStopping    NodeStatus = "stopping"
	NodeStatusMaintenance NodeStatus = "maintenance"
)

// NodeConfig 节点配置
type NodeConfig struct {
	NodeID     int64
	ConfigData map[string]interface{}
	ConfigPath string
	VpnType    string
	NodePort   int
	Endpoint   *string
}

// NodeMetrics 节点性能指标
type NodeMetrics struct {
	NodeID      int64
	CPUUsage    float64
	MemoryUsage float64
	NetworkIn   float64
	NetworkOut  float64
	Connections int
	Errors      int
	Timestamp   int64
}

// NodeOperation 节点操作类型
type NodeOperation string

const (
	NodeOpStart   NodeOperation = "start"
	NodeOpStop    NodeOperation = "stop"
	NodeOpRestart NodeOperation = "restart"
	NodeOpDelete  NodeOperation = "delete"
)

// Auth 认证信息
type Auth struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

// IsExpired 判断是否过期 (容忍 30 秒提前刷新)
func (a *Auth) IsExpired(now int64) bool {
	if a == nil || a.ExpiresAt == 0 {
		return false
	} // 0 表示未知有效期
	return now+30 >= a.ExpiresAt
}

// LoginResult 登录结果
type LoginResult struct {
	User      *User
	Auth      *Auth
	ServerURL string
}

// UserSession 用户会话
type UserSession struct {
	User        *User
	Auth        *Auth
	ServerURL   string
	CurrentZone *Zone
}

// SystemStatus 系统状态
type SystemStatus struct {
	CPUUsage     float64 `json:"cpu_usage"`
	MemoryUsage  float64 `json:"memory_usage"`
	MemoryTotal  uint64  `json:"memory_total"`
	DiskUsage    float64 `json:"disk_usage"`
	DiskTotal    uint64  `json:"disk_total"`
	NetworkIn    uint64  `json:"network_in"`
	NetworkOut   uint64  `json:"network_out"`
	Uptime       uint64  `json:"uptime"`
	LoadAverage  float64 `json:"load_average"`
	ProcessCount int     `json:"process_count"`
	Timestamp    int64   `json:"timestamp"`
}

// NodeStatusDetail 节点详细状态
type NodeStatusDetail struct {
	Node         *Node        `json:"node"`
	LocalStatus  NodeStatus   `json:"local_status"`
	ServerStatus NodeStatus   `json:"server_status"`
	ProcessID    int          `json:"process_id"`
	StartTime    int64        `json:"start_time"`
	Metrics      *NodeMetrics `json:"metrics"`
	Config       *NodeConfig  `json:"config"`
	Logs         []LogEntry   `json:"logs"`
	IsRunning    bool         `json:"is_running"`
	LastSync     int64        `json:"last_sync"`
}

// LogEntry 日志条目
type LogEntry struct {
	Timestamp int64    `json:"timestamp"`
	Level     LogLevel `json:"level"`
	Message   string   `json:"message"`
	Source    string   `json:"source"`
}

// LogLevel 日志级别
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// DeviceInfo 设备信息
type DeviceInfo struct {
	NodeID       int64  `json:"node_id"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	CPUCores     int    `json:"cpu_cores"`
	MemoryTotal  uint64 `json:"memory_total"`
	DiskTotal    uint64 `json:"disk_total"`
	IPAddress    string `json:"ip_address"`
	MacAddress   string `json:"mac_address"`
	Version      string `json:"version"`
	UpdatedAt    int64  `json:"updated_at"`
}
