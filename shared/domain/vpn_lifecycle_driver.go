package domain

// DriverCaps 声明某个 driver 实际支持的能力。
//
// 存在的意义：各驱动能力并不对齐（gnb 有 P2P 索引发现与内建 proxy 标志，
// wireguard 没有等价物）。调用方必须按本结构分支，而不是假设「换了驱动功能一样」。
type DriverCaps struct {
	// HotReload 支持配置热生效（无需冷启）。gnb 侧依赖 gnb_ctl -w core.flag=2，
	// 该能力在目标 gnb 版本上尚未验证，故实现时应如实返回 false。
	HotReload bool `json:"hot_reload"`

	// Discovery 支持索引/发现服务（对应 VPNBundle.Discovery）。
	Discovery bool `json:"discovery"`

	// Proxy 支持驱动级代理开关（对应 VPNBundle.Flags.Proxy）。
	Proxy bool `json:"proxy"`

	// CustomKeyUpload 支持上传客户端自生成密钥（而非仅服务端生成）。
	CustomKeyUpload bool `json:"custom_key_upload"`
}

// VPNLifecycleDriver 在只读能力（VPNDriver）之外，补齐「写配置 + 生命周期」。
//
// 落地顺序见 spec/driver-abstraction.md §5（**R0–R9**）：
// 本接口目前**只定义契约，尚未有实现**。现有 GNBDriver 仍是只读的，
// 因此调用方不要把它当作 VPNDriver 的替代，而应按需做类型断言：
//
//	if lc, ok := d.(domain.VPNLifecycleDriver); ok { ... }
//
// 生命周期权威的收敛是 **R5**（"收敛配置写入权与生命周期权威；旧 node/route refresh
// 改为委托 Apply"），排在 R2（schema v2）与 R4（原子 Apply：stage→validate→write→
// reload→health-check→commit/rollback）之后。该 spec 明确写着"在 R2–R6 完成前，
// 不应继续实现…否则会把错误抽象固化进代码"——所以**不要在 R2–R4 之前实现本接口**。
//
// （本注释原先引用 S1/S3/S4/S5，但 spec §5 已改写成 R0–R9；那是过期引用，已更正。）
type VPNLifecycleDriver interface {
	// Capabilities 返回驱动能力，调用方据此分支。
	Capabilities() DriverCaps

	// WriteConfig 将驱动无关的 bundle 翻译并落盘为原生配置。
	//
	// 实现约束：不得改变既有文件权限——security/*.private 为 0600。
	WriteConfig(b *VPNBundle) error

	// Start 启动当前节点。
	Start() error

	// Stop 停止当前节点。
	Stop() error

	// Reload 重载配置：优先热重启，失败回落冷启。
	//
	// 注意：gnb 的热重启机制（gnb_ctl -b gnb.map -w core.flag=2）在仓库中
	// 尚无任何实现，实现本接口时应退化为 Stop + Start，并且 Capabilities().HotReload
	// 必须如实返回 false（见 spec/driver-abstraction.md §5 的 R5）。
	Reload() error

	// Status 返回运行时状态。
	Status() (*DriverStatus, error)
}
