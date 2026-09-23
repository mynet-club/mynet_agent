package domain

// VPNDriverType VPN驱动类型
type VPNDriverType string

const (
	DriverTypeGNB       VPNDriverType = "gnb"
	DriverTypeWireGuard VPNDriverType = "wireguard"
)

// VPNDriver VPN驱动接口 (抽象层)
// 用于解耦具体实现，支持 GNB、WireGuard 等多种 driver
type VPNDriver interface {
	// GetDriverType 获取驱动类型
	GetDriverType() VPNDriverType

	// IsAvailable 检查驱动是否可用
	IsAvailable() bool

	// IsProcessAlive 检查VPN进程是否运行
	IsProcessAlive() bool

	// GetAllPeers 获取所有对等节点
	GetAllPeers() ([]*VPNPeer, error)

	// GetOnlinePeers 获取在线节点
	GetOnlinePeers() ([]*VPNPeer, error)

	// GetPeer 获取单个节点信息
	GetPeer(peerID int64) (*VPNPeer, error)

	// GetLocalPeer 获取本地节点信息
	GetLocalPeer() (*VPNPeer, error)

	// GetNetworkSummary 获取网络摘要
	GetNetworkSummary() (*VPNNetworkSummary, error)

	// Refresh 刷新数据 (从map文件/wg命令等)
	Refresh() error
}

// VPNDriverFactory 驱动工厂接口
type VPNDriverFactory interface {
	// CreateDriver 创建指定类型的驱动
	CreateDriver(driverType VPNDriverType, nodeID int64) (VPNDriver, error)

	// DetectDriver 自动检测当前使用的驱动
	DetectDriver(nodeID int64) (VPNDriver, error)
}
