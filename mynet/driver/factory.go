package driver

import (
	"fmt"

	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

// Factory — A-4：按 type 创建驱动；DetectDriver 自动识别。
type Factory struct{}

func NewFactory() *Factory { return &Factory{} }

// Create 同时满足 VPNDriver 与 VPNLifecycleDriver。
func (f *Factory) Create(driverType domain.VPNDriverType, nodeID int64) (domain.VPNDriver, error) {
	switch driverType {
	case domain.DriverTypeWireGuard:
		return NewWireGuardDriver("wg0", nodeID), nil
	case domain.DriverTypeGNB:
		return NewGNBDriver(nodeID), nil
	default:
		return nil, fmt.Errorf("unknown driver type %q", driverType)
	}
}

// DetectDriver 优先活动接口：wg0 有 wg show → wireguard；否则 gnb_ctl 可用 → gnb。
func (f *Factory) DetectDriver(nodeID int64) (domain.VPNDriver, error) {
	if d := NewWireGuardDriver("wg0", nodeID); d.IsAvailable() {
		return d, nil
	}
	if d := NewGNBDriver(nodeID); d.IsAvailable() {
		return d, nil
	}
	return nil, fmt.Errorf("no active vpn driver for node %d", nodeID)
}

// AsLifecycle 编译期/运行期断言：创建出的驱动必须满足 VPNLifecycleDriver。
func AsLifecycle(d domain.VPNDriver) (domain.VPNLifecycleDriver, error) {
	lc, ok := d.(domain.VPNLifecycleDriver)
	if !ok {
		return nil, fmt.Errorf("driver %T does not implement VPNLifecycleDriver", d)
	}
	return lc, nil
}
