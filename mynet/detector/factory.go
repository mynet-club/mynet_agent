package detector

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

// DetectorFactory 检测器工厂
type DetectorFactory struct {
	systemDetectors   map[string]func() SystemDetector
	routingDetectors  map[string]func(SystemDetector) RoutingDetector
	firewallDetectors map[string]func(SystemDetector) FirewallDetector
	serviceDetectors  map[string]func(SystemDetector) ServiceDetector
}

// NewDetectorFactory 创建检测器工厂
func NewDetectorFactory() *DetectorFactory {
	factory := &DetectorFactory{
		systemDetectors:   make(map[string]func() SystemDetector),
		routingDetectors:  make(map[string]func(SystemDetector) RoutingDetector),
		firewallDetectors: make(map[string]func(SystemDetector) FirewallDetector),
		serviceDetectors:  make(map[string]func(SystemDetector) ServiceDetector),
	}

	// 注册默认检测器
	factory.registerDefaultDetectors()

	return factory
}

// registerDefaultDetectors 注册默认检测器
func (f *DetectorFactory) registerDefaultDetectors() {
	// 根据构建标签调用相应的平台特定注册函数
	f.registerPlatformSpecificDetectors()
}

// RegisterSystemDetector 注册系统检测器
func (f *DetectorFactory) RegisterSystemDetector(platform string, factory func() SystemDetector) {
	f.systemDetectors[platform] = factory
}

// RegisterRoutingDetector 注册路由检测器
func (f *DetectorFactory) RegisterRoutingDetector(platform string, factory func(SystemDetector) RoutingDetector) {
	f.routingDetectors[platform] = factory
}

// RegisterFirewallDetector 注册防火墙检测器
func (f *DetectorFactory) RegisterFirewallDetector(platform string, factory func(SystemDetector) FirewallDetector) {
	f.firewallDetectors[platform] = factory
}

// RegisterServiceDetector 注册服务检测器
func (f *DetectorFactory) RegisterServiceDetector(platform string, factory func(SystemDetector) ServiceDetector) {
	f.serviceDetectors[platform] = factory
}

// CreateSystemDetector 创建系统检测器
func (f *DetectorFactory) CreateSystemDetector(platform string) (SystemDetector, error) {
	factory, exists := f.systemDetectors[platform]
	if !exists {
		return nil, fmt.Errorf("system detector not found for platform: %s", platform)
	}

	detector := factory()
	if !detector.IsSupported() {
		return nil, fmt.Errorf("system detector not supported for platform: %s", platform)
	}

	return detector, nil
}

// CreateRoutingDetector 创建路由检测器
func (f *DetectorFactory) CreateRoutingDetector(platform string, systemDetector SystemDetector) (RoutingDetector, error) {
	factory, exists := f.routingDetectors[platform]
	if !exists {
		return nil, fmt.Errorf("routing detector not found for platform: %s", platform)
	}

	detector := factory(systemDetector)
	if !detector.IsSupported() {
		return nil, fmt.Errorf("routing detector not supported for platform: %s", platform)
	}

	return detector, nil
}

// CreateFirewallDetector 创建防火墙检测器
func (f *DetectorFactory) CreateFirewallDetector(platform string, systemDetector SystemDetector) (FirewallDetector, error) {
	factory, exists := f.firewallDetectors[platform]
	if !exists {
		return nil, fmt.Errorf("firewall detector not found for platform: %s", platform)
	}

	detector := factory(systemDetector)
	if !detector.IsSupported() {
		return nil, fmt.Errorf("firewall detector not supported for platform: %s", platform)
	}

	return detector, nil
}

// CreateServiceDetector 创建服务检测器
func (f *DetectorFactory) CreateServiceDetector(platform string, systemDetector SystemDetector) (ServiceDetector, error) {
	factory, exists := f.serviceDetectors[platform]
	if !exists {
		return nil, fmt.Errorf("service detector not found for platform: %s", platform)
	}

	detector := factory(systemDetector)
	if !detector.IsSupported() {
		return nil, fmt.Errorf("service detector not supported for platform: %s", platform)
	}

	return detector, nil
}

// CreateUnifiedDetector 创建统一检测器
func (f *DetectorFactory) CreateUnifiedDetector(platform string) (UnifiedDetector, error) {
	// 创建系统检测器
	systemDetector, err := f.CreateSystemDetector(platform)
	if err != nil {
		return nil, fmt.Errorf("failed to create system detector: %w", err)
	}

	// 创建路由检测器
	routingDetector, err := f.CreateRoutingDetector(platform, systemDetector)
	if err != nil {
		return nil, fmt.Errorf("failed to create routing detector: %w", err)
	}

	// 创建防火墙检测器
	firewallDetector, err := f.CreateFirewallDetector(platform, systemDetector)
	if err != nil {
		return nil, fmt.Errorf("failed to create firewall detector: %w", err)
	}

	// 创建服务检测器
	serviceDetector, err := f.CreateServiceDetector(platform, systemDetector)
	if err != nil {
		return nil, fmt.Errorf("failed to create service detector: %w", err)
	}

	// 创建缓存检测器
	cachedDetector := NewCachedDetector(
		systemDetector,
		routingDetector,
		firewallDetector,
		serviceDetector,
	)

	return cachedDetector, nil
}

// CreateAutoDetector 自动创建适合当前平台的检测器
func (f *DetectorFactory) CreateAutoDetector() (UnifiedDetector, error) {
	platform := runtime.GOOS

	// 尝试创建当前平台的检测器
	detector, err := f.CreateUnifiedDetector(platform)
	if err == nil {
		return detector, nil
	}

	// 如果失败，尝试通用的 Linux 检测器（适用于大多数 Unix 系统）
	if platform != "linux" {
		if detector, err := f.CreateUnifiedDetector("linux"); err == nil {
			return detector, nil
		}
	}

	return nil, fmt.Errorf("no suitable detector found for platform: %s", platform)
}

// GetSupportedPlatforms 获取支持的平台列表
func (f *DetectorFactory) GetSupportedPlatforms() []string {
	platforms := make([]string, 0, len(f.systemDetectors))
	for platform := range f.systemDetectors {
		platforms = append(platforms, platform)
	}
	return platforms
}

// ValidatePlatform 验证平台是否支持
func (f *DetectorFactory) ValidatePlatform(platform string) error {
	// 检查系统检测器
	if _, exists := f.systemDetectors[platform]; !exists {
		return fmt.Errorf("system detector not available for platform: %s", platform)
	}

	// 检查路由检测器
	if _, exists := f.routingDetectors[platform]; !exists {
		return fmt.Errorf("routing detector not available for platform: %s", platform)
	}

	// 检查防火墙检测器
	if _, exists := f.firewallDetectors[platform]; !exists {
		return fmt.Errorf("firewall detector not available for platform: %s", platform)
	}

	// 检查服务检测器
	if _, exists := f.serviceDetectors[platform]; !exists {
		return fmt.Errorf("service detector not available for platform: %s", platform)
	}

	return nil
}

// DefaultDetectorManager 默认检测器管理器
type DefaultDetectorManager struct {
	factory  *DetectorFactory
	detector UnifiedDetector
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewDefaultDetectorManager 创建默认检测器管理器
func NewDefaultDetectorManager() (*DefaultDetectorManager, error) {
	factory := NewDetectorFactory()

	detector, err := factory.CreateAutoDetector()
	if err != nil {
		return nil, fmt.Errorf("failed to create auto detector: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	manager := &DefaultDetectorManager{
		factory:  factory,
		detector: detector,
		ctx:      ctx,
		cancel:   cancel,
	}

	// 启动缓存清理任务
	if cachedDetector, ok := detector.(*CachedDetector); ok {
		go cachedDetector.StartCacheCleanup(ctx)
	}

	return manager, nil
}

// GetDetector 获取检测器
func (m *DefaultDetectorManager) GetDetector() UnifiedDetector {
	return m.detector
}

// GetFactory 获取工厂
func (m *DefaultDetectorManager) GetFactory() *DetectorFactory {
	return m.factory
}

// SetCacheExpiry 设置缓存过期时间
func (m *DefaultDetectorManager) SetCacheExpiry(duration time.Duration) {
	m.detector.SetCacheExpiry(duration)
}

// ClearCache 清除缓存
func (m *DefaultDetectorManager) ClearCache() {
	m.detector.ClearCache()
}

// Shutdown 关闭管理器
func (m *DefaultDetectorManager) Shutdown() {
	if m.cancel != nil {
		m.cancel()
	}
}

// CreateDetectionContext 创建检测上下文
func (m *DefaultDetectorManager) CreateDetectionContext(configDir, nodeID string, timeout time.Duration, debug bool) DetectionContext {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		// 注意: 这里没有调用 cancel，调用者需要自己管理
		_ = cancel
	}

	return DetectionContext{
		Context:   ctx,
		Timeout:   timeout,
		ConfigDir: configDir,
		NodeID:    nodeID,
		Debug:     debug,
	}
}

// 全局默认管理器实例
var defaultManager *DefaultDetectorManager

// GetDefaultDetector 获取默认检测器
func GetDefaultDetector() (UnifiedDetector, error) {
	if defaultManager == nil {
		manager, err := NewDefaultDetectorManager()
		if err != nil {
			return nil, err
		}
		defaultManager = manager
	}

	return defaultManager.GetDetector(), nil
}

// ShutdownDefaultDetector 关闭默认检测器
func ShutdownDefaultDetector() {
	if defaultManager != nil {
		defaultManager.Shutdown()
		defaultManager = nil
	}
}
