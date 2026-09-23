package domain

import "context"

// Repository interfaces - 领域层的抽象接口，不依赖具体实现

// AuthService 认证服务接口 - 外部API服务
type AuthService interface {
	Login(ctx context.Context, serverURL, email, password string) (*LoginResult, error)
	RefreshToken(ctx context.Context, refreshToken string) (*Auth, error)
	VerifyToken(ctx context.Context, accessToken string) error
}

// UserRepository 用户仓储接口
type UserRepository interface {
	Save(ctx context.Context, user *User) error
	FindByEmail(ctx context.Context, email string) (*User, error)
	Delete(ctx context.Context, userID string) error
}

// ZoneRepository 区域仓储接口
type ZoneRepository interface {
	FindAll(ctx context.Context) ([]*Zone, error)
	FindByID(ctx context.Context, id int64) (*Zone, error)
	Save(ctx context.Context, zone *Zone) error
}

// NodeRepository 节点仓储接口
type NodeRepository interface {
	FindByZoneID(ctx context.Context, zoneID int64) ([]*Node, error)
	FindByID(ctx context.Context, nodeID int64) (*Node, error)
	Save(ctx context.Context, node *Node) error
	Delete(ctx context.Context, nodeID int64) error
	UpdateStatus(ctx context.Context, nodeID int64, status NodeStatus) error
	FindAll(ctx context.Context) ([]*Node, error)
}

// NodeConfigRepository 节点配置仓储接口
type NodeConfigRepository interface {
	SaveConfig(ctx context.Context, config *NodeConfig) error
	GetConfig(ctx context.Context, nodeID int64) (*NodeConfig, error)
	DeleteConfig(ctx context.Context, nodeID int64) error
}

// NodeMetricsRepository 节点指标仓储接口
type NodeMetricsRepository interface {
	SaveMetrics(ctx context.Context, metrics *NodeMetrics) error
	GetLatestMetrics(ctx context.Context, nodeID int64) (*NodeMetrics, error)
	GetMetricsHistory(ctx context.Context, nodeID int64, limit int) ([]*NodeMetrics, error)
}

// SystemStatusRepository 系统状态仓储接口
type SystemStatusRepository interface {
	GetCurrentStatus(ctx context.Context) (*SystemStatus, error)
	SaveStatus(ctx context.Context, status *SystemStatus) error
	GetStatusHistory(ctx context.Context, limit int) ([]*SystemStatus, error)
}

// DeviceInfoRepository 设备信息仓储接口
type DeviceInfoRepository interface {
	GetDeviceInfo(ctx context.Context, nodeID int64) (*DeviceInfo, error)
	SaveDeviceInfo(ctx context.Context, info *DeviceInfo) error
	UpdateDeviceInfo(ctx context.Context, nodeID int64, info *DeviceInfo) error
}

// LogRepository 日志仓储接口
type LogRepository interface {
	GetLogs(ctx context.Context, nodeID int64, limit int) ([]*LogEntry, error)
	SaveLog(ctx context.Context, entry *LogEntry) error
	ClearLogs(ctx context.Context, nodeID int64) error
}

// NodeService 节点服务接口 - 外部服务
type NodeService interface {
	GetNodes(ctx context.Context, zoneID int64) ([]*Node, error)
	GetNodeConfig(ctx context.Context, nodeID int64) (*NodeConfig, error)
	StartNode(ctx context.Context, nodeID int64) error
	StopNode(ctx context.Context, nodeID int64) error
	GetNodeMetrics(ctx context.Context, nodeID int64) (*NodeMetrics, error)
}

// ZoneService 区域外部服务接口（例如 API）
type ZoneService interface {
	GetZones(ctx context.Context) ([]*Zone, error)
}

// StatusService 状态服务接口 - 外部服务
type StatusService interface {
	UpdateNodeStatus(ctx context.Context, nodeID int64, status NodeStatus) error
	ReportDeviceInfo(ctx context.Context, nodeID int64, info *DeviceInfo) error
	SyncNodeStatus(ctx context.Context, nodeID int64) (*NodeStatusDetail, error)
}

// SystemMonitorService 系统监控服务接口 - 外部服务
type SystemMonitorService interface {
	GetSystemStatus(ctx context.Context) (*SystemStatus, error)
	GetProcessStatus(ctx context.Context, nodeID int64) (bool, int, error)
	StartProcess(ctx context.Context, nodeID int64) error
	StopProcess(ctx context.Context, nodeID int64) error
	GetDeviceInfo(ctx context.Context) (*DeviceInfo, error)
}

// SessionRepository 会话仓储接口
type SessionRepository interface {
	SaveSession(ctx context.Context, session *UserSession) error
	GetSession(ctx context.Context) (*UserSession, error)
	ClearSession(ctx context.Context) error
}
