package detector

import (
	"context"
	"time"
)

// SystemInfo 系统基础信息
type SystemInfo struct {
	OS           string            `json:"os"`           // 操作系统类型: linux, openwrt, darwin, windows
	Distribution string            `json:"distribution"` // 发行版: ubuntu, centos, openwrt, etc
	Version      string            `json:"version"`      // 版本号
	Architecture string            `json:"architecture"` // 架构: amd64, arm64, etc
	Kernel       string            `json:"kernel"`       // 内核版本
	Hostname     string            `json:"hostname"`     // 主机名
	Uptime       time.Duration     `json:"uptime"`       // 系统运行时间
	Properties   map[string]string `json:"properties"`   // 额外属性
}

// NetworkInterface 网络接口信息
type NetworkInterface struct {
	Name      string   `json:"name"`       // 接口名称
	Type      string   `json:"type"`       // 接口类型: ethernet, wifi, vpn, etc
	State     string   `json:"state"`      // 状态: up, down
	IPs       []string `json:"ips"`        // IP地址列表
	MTU       int      `json:"mtu"`        // MTU值
	MAC       string   `json:"mac"`        // MAC地址
	IsDefault bool     `json:"is_default"` // 是否是默认接口
}

// RoutingInfo 路由信息
type RoutingInfo struct {
	DefaultGateway string             `json:"default_gateway"` // 默认网关
	DefaultIface   string             `json:"default_iface"`   // 默认接口
	Interfaces     []NetworkInterface `json:"interfaces"`      // 网络接口列表
	Routes         []RouteEntry       `json:"routes"`          // 路由条目
	Statistics     RouteStatistics    `json:"statistics"`      // 统计信息
}

// RouteEntry 路由条目
type RouteEntry struct {
	Destination string `json:"destination"` // 目标网络
	Gateway     string `json:"gateway"`     // 网关
	Interface   string `json:"interface"`   // 接口
	Metric      int    `json:"metric"`      // 权重
	Protocol    string `json:"protocol"`    // 协议
	Scope       string `json:"scope"`       // 范围
	Type        string `json:"type"`        // 类型
	Flags       string `json:"flags"`       // 标志
	IsActive    bool   `json:"is_active"`   // 是否活跃
}

// RouteStatistics 路由统计信息
type RouteStatistics struct {
	TotalInterfaces  int `json:"total_interfaces"`  // 接口总数
	TotalRoutes      int `json:"total_routes"`      // 路由总数
	NodeRoutes       int `json:"node_routes"`       // 节点路由数
	ActiveInterfaces int `json:"active_interfaces"` // 活跃接口数
}

// FirewallInfo 防火墙信息
type FirewallInfo struct {
	Type        string                 `json:"type"`         // 防火墙类型: iptables, fw3, pf, etc
	Enabled     bool                   `json:"enabled"`      // 是否启用
	Rules       []FirewallRule         `json:"rules"`        // 规则列表
	Zones       []FirewallZone         `json:"zones"`        // 区域配置
	PortStates  []PortState            `json:"port_states"`  // 端口状态
	ChainStates map[string]string      `json:"chain_states"` // 链状态
	Properties  map[string]interface{} `json:"properties"`   // 额外属性
}

// FirewallRule 防火墙规则
type FirewallRule struct {
	Chain     string `json:"chain"`     // 链名
	Target    string `json:"target"`    // 目标动作
	Protocol  string `json:"protocol"`  // 协议
	Source    string `json:"source"`    // 源地址
	Dest      string `json:"dest"`      // 目标地址
	Port      string `json:"port"`      // 端口
	Interface string `json:"interface"` // 接口
	State     string `json:"state"`     // 状态
	Comment   string `json:"comment"`   // 注释
}

// FirewallZone 防火墙区域
type FirewallZone struct {
	Name      string   `json:"name"`       // 区域名称
	Networks  []string `json:"networks"`   // 网络列表
	Input     string   `json:"input"`      // 输入策略
	Output    string   `json:"output"`     // 输出策略
	Forward   string   `json:"forward"`    // 转发策略
	Masq      bool     `json:"masq"`       // 是否启用伪装
	IsHealthy bool     `json:"is_healthy"` // 配置是否健康
}

// PortState 端口状态
type PortState struct {
	Port     int    `json:"port"`     // 端口号
	Protocol string `json:"protocol"` // 协议
	State    string `json:"state"`    // 状态: LISTEN, CLOSED, etc
	Process  string `json:"process"`  // 进程信息
	Firewall string `json:"firewall"` // 防火墙状态
}

// ServiceInfo 服务信息
type ServiceInfo struct {
	Name              string            `json:"name"`               // 服务名称
	Type              string            `json:"type"`               // 服务管理器类型
	IsInstalled       bool              `json:"is_installed"`       // 是否已安装
	IsEnabled         bool              `json:"is_enabled"`         // 是否启用自启动
	IsRunning         bool              `json:"is_running"`         // 是否正在运行
	ServicePath       string            `json:"service_path"`       // 服务文件路径
	ConfigPath        string            `json:"config_path"`        // 配置文件路径
	ExecutablePath    string            `json:"executable_path"`    // 可执行文件路径
	WorkingDir        string            `json:"working_dir"`        // 工作目录
	ServiceUser       string            `json:"service_user"`       // 运行用户
	ConfigConsistency bool              `json:"config_consistency"` // 配置一致性
	ErrorMsg          string            `json:"error_msg"`          // 错误信息
	Properties        map[string]string `json:"properties"`         // 额外属性
}

// DetectionContext 检测上下文
type DetectionContext struct {
	Context   context.Context `json:"-"`          // 上下文
	Timeout   time.Duration   `json:"timeout"`    // 超时时间
	ConfigDir string          `json:"config_dir"` // 配置目录
	NodeID    string          `json:"node_id"`    // 节点ID
	Debug     bool            `json:"debug"`      // 调试模式
}

// SystemDetector 系统检测器接口
type SystemDetector interface {
	// DetectSystem 检测系统基础信息
	DetectSystem(ctx DetectionContext) (*SystemInfo, error)

	// GetCacheKey 获取缓存键
	GetCacheKey() string

	// IsSupported 检查是否支持当前系统
	IsSupported() bool
}

// RoutingDetector 路由检测器接口
type RoutingDetector interface {
	// DetectRouting 检测路由信息
	DetectRouting(ctx DetectionContext) (*RoutingInfo, error)

	// GetCacheKey 获取缓存键
	GetCacheKey() string

	// IsSupported 检查是否支持当前系统
	IsSupported() bool
}

// FirewallDetector 防火墙检测器接口
type FirewallDetector interface {
	// DetectFirewall 检测防火墙信息
	DetectFirewall(ctx DetectionContext) (*FirewallInfo, error)

	// GetCacheKey 获取缓存键
	GetCacheKey() string

	// IsSupported 检查是否支持当前系统
	IsSupported() bool
}

// ServiceDetector 服务检测器接口
type ServiceDetector interface {
	// DetectService 检测服务信息
	DetectService(ctx DetectionContext, serviceName string) (*ServiceInfo, error)

	// GetCacheKey 获取缓存键
	GetCacheKey(serviceName string) string

	// IsSupported 检查是否支持当前系统
	IsSupported() bool
}

// DetectionResult 检测结果
type DetectionResult struct {
	System   *SystemInfo   `json:"system"`    // 系统信息
	Routing  *RoutingInfo  `json:"routing"`   // 路由信息
	Firewall *FirewallInfo `json:"firewall"`  // 防火墙信息
	Service  *ServiceInfo  `json:"service"`   // 服务信息
	Error    error         `json:"error"`     // 错误信息
	CachedAt time.Time     `json:"cached_at"` // 缓存时间
}

// UnifiedDetector 统一检测器接口
type UnifiedDetector interface {
	// DetectAll 检测所有信息
	DetectAll(ctx DetectionContext) (*DetectionResult, error)

	// DetectSystem 检测系统信息
	DetectSystem(ctx DetectionContext) (*SystemInfo, error)

	// DetectRouting 检测路由信息
	DetectRouting(ctx DetectionContext) (*RoutingInfo, error)

	// DetectFirewall 检测防火墙信息
	DetectFirewall(ctx DetectionContext) (*FirewallInfo, error)

	// DetectService 检测服务信息
	DetectService(ctx DetectionContext, serviceName string) (*ServiceInfo, error)

	// ClearCache 清除缓存
	ClearCache()

	// SetCacheExpiry 设置缓存过期时间
	SetCacheExpiry(duration time.Duration)
}
