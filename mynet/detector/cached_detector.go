package detector

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// CachedDetector 带缓存的统一检测器实现
type CachedDetector struct {
	systemDetector   SystemDetector
	routingDetector  RoutingDetector
	firewallDetector FirewallDetector
	serviceDetector  ServiceDetector

	// 缓存
	cache       map[string]*cacheEntry
	cacheMutex  sync.RWMutex
	cacheExpiry time.Duration
}

// cacheEntry 缓存条目
type cacheEntry struct {
	result    interface{}
	cachedAt  time.Time
	expiresAt time.Time
}

// NewCachedDetector 创建带缓存的统一检测器
func NewCachedDetector(
	systemDetector SystemDetector,
	routingDetector RoutingDetector,
	firewallDetector FirewallDetector,
	serviceDetector ServiceDetector,
) *CachedDetector {
	return &CachedDetector{
		systemDetector:   systemDetector,
		routingDetector:  routingDetector,
		firewallDetector: firewallDetector,
		serviceDetector:  serviceDetector,
		cache:            make(map[string]*cacheEntry),
		cacheExpiry:      30 * time.Second, // 默认30秒缓存
	}
}

// DetectAll 检测所有信息
func (d *CachedDetector) DetectAll(ctx DetectionContext) (*DetectionResult, error) {
	result := &DetectionResult{
		CachedAt: time.Now(),
	}

	var wg sync.WaitGroup
	var mu sync.Mutex

	// 并发检测各种信息
	wg.Add(3) // system, routing, firewall

	// 检测系统信息
	go func() {
		defer wg.Done()
		if system, err := d.DetectSystem(ctx); err != nil {
			mu.Lock()
			if result.Error == nil {
				result.Error = fmt.Errorf("system detection failed: %w", err)
			}
			mu.Unlock()
		} else {
			mu.Lock()
			result.System = system
			mu.Unlock()
		}
	}()

	// 检测路由信息
	go func() {
		defer wg.Done()
		if routing, err := d.DetectRouting(ctx); err != nil {
			mu.Lock()
			if result.Error == nil {
				result.Error = fmt.Errorf("routing detection failed: %w", err)
			}
			mu.Unlock()
		} else {
			mu.Lock()
			result.Routing = routing
			mu.Unlock()
		}
	}()

	// 检测防火墙信息
	go func() {
		defer wg.Done()
		if firewall, err := d.DetectFirewall(ctx); err != nil {
			mu.Lock()
			if result.Error == nil {
				result.Error = fmt.Errorf("firewall detection failed: %w", err)
			}
			mu.Unlock()
		} else {
			mu.Lock()
			result.Firewall = firewall
			mu.Unlock()
		}
	}()

	wg.Wait()

	return result, result.Error
}

// DetectSystem 检测系统信息（带缓存）
func (d *CachedDetector) DetectSystem(ctx DetectionContext) (*SystemInfo, error) {
	if d.systemDetector == nil || !d.systemDetector.IsSupported() {
		return nil, fmt.Errorf("system detector not supported")
	}

	cacheKey := d.systemDetector.GetCacheKey()

	// 检查缓存
	if cached := d.getCache(cacheKey); cached != nil {
		if systemInfo, ok := cached.(*SystemInfo); ok {
			return systemInfo, nil
		}
	}

	// 执行检测
	systemInfo, err := d.systemDetector.DetectSystem(ctx)
	if err != nil {
		return nil, err
	}

	// 缓存结果
	d.setCache(cacheKey, systemInfo)

	return systemInfo, nil
}

// DetectRouting 检测路由信息（带缓存）
func (d *CachedDetector) DetectRouting(ctx DetectionContext) (*RoutingInfo, error) {
	if d.routingDetector == nil || !d.routingDetector.IsSupported() {
		return nil, fmt.Errorf("routing detector not supported")
	}

	cacheKey := d.routingDetector.GetCacheKey()

	// 检查缓存
	if cached := d.getCache(cacheKey); cached != nil {
		if routingInfo, ok := cached.(*RoutingInfo); ok {
			return routingInfo, nil
		}
	}

	// 执行检测
	routingInfo, err := d.routingDetector.DetectRouting(ctx)
	if err != nil {
		return nil, err
	}

	// 缓存结果
	d.setCache(cacheKey, routingInfo)

	return routingInfo, nil
}

// DetectFirewall 检测防火墙信息（带缓存）
func (d *CachedDetector) DetectFirewall(ctx DetectionContext) (*FirewallInfo, error) {
	if d.firewallDetector == nil || !d.firewallDetector.IsSupported() {
		return nil, fmt.Errorf("firewall detector not supported")
	}

	cacheKey := d.firewallDetector.GetCacheKey()

	// 检查缓存
	if cached := d.getCache(cacheKey); cached != nil {
		if firewallInfo, ok := cached.(*FirewallInfo); ok {
			return firewallInfo, nil
		}
	}

	// 执行检测
	firewallInfo, err := d.firewallDetector.DetectFirewall(ctx)
	if err != nil {
		return nil, err
	}

	// 缓存结果
	d.setCache(cacheKey, firewallInfo)

	return firewallInfo, nil
}

// DetectService 检测服务信息（带缓存）
func (d *CachedDetector) DetectService(ctx DetectionContext, serviceName string) (*ServiceInfo, error) {
	if d.serviceDetector == nil || !d.serviceDetector.IsSupported() {
		return nil, fmt.Errorf("service detector not supported")
	}

	cacheKey := d.serviceDetector.GetCacheKey(serviceName)

	// 检查缓存
	if cached := d.getCache(cacheKey); cached != nil {
		if serviceInfo, ok := cached.(*ServiceInfo); ok {
			return serviceInfo, nil
		}
	}

	// 执行检测
	serviceInfo, err := d.serviceDetector.DetectService(ctx, serviceName)
	if err != nil {
		return nil, err
	}

	// 缓存结果
	d.setCache(cacheKey, serviceInfo)

	return serviceInfo, nil
}

// ClearCache 清除所有缓存
func (d *CachedDetector) ClearCache() {
	d.cacheMutex.Lock()
	defer d.cacheMutex.Unlock()

	d.cache = make(map[string]*cacheEntry)
}

// SetCacheExpiry 设置缓存过期时间
func (d *CachedDetector) SetCacheExpiry(duration time.Duration) {
	d.cacheMutex.Lock()
	defer d.cacheMutex.Unlock()

	d.cacheExpiry = duration
}

// getCache 获取缓存
func (d *CachedDetector) getCache(key string) interface{} {
	d.cacheMutex.RLock()
	defer d.cacheMutex.RUnlock()

	entry, exists := d.cache[key]
	if !exists {
		return nil
	}

	// 检查是否过期
	if time.Now().After(entry.expiresAt) {
		delete(d.cache, key)
		return nil
	}

	return entry.result
}

// setCache 设置缓存
func (d *CachedDetector) setCache(key string, result interface{}) {
	d.cacheMutex.Lock()
	defer d.cacheMutex.Unlock()

	now := time.Now()
	d.cache[key] = &cacheEntry{
		result:    result,
		cachedAt:  now,
		expiresAt: now.Add(d.cacheExpiry),
	}
}

// cleanExpiredCache 清理过期缓存（后台任务）
func (d *CachedDetector) cleanExpiredCache() {
	d.cacheMutex.Lock()
	defer d.cacheMutex.Unlock()

	now := time.Now()
	for key, entry := range d.cache {
		if now.After(entry.expiresAt) {
			delete(d.cache, key)
		}
	}
}

// StartCacheCleanup 启动缓存清理任务
func (d *CachedDetector) StartCacheCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.cleanExpiredCache()
		}
	}
}

// GetCacheStats 获取缓存统计
func (d *CachedDetector) GetCacheStats() map[string]interface{} {
	d.cacheMutex.RLock()
	defer d.cacheMutex.RUnlock()

	stats := make(map[string]interface{})
	stats["total_entries"] = len(d.cache)
	stats["expiry_duration"] = d.cacheExpiry.String()

	// 计算过期条目
	now := time.Now()
	expired := 0
	for _, entry := range d.cache {
		if now.After(entry.expiresAt) {
			expired++
		}
	}
	stats["expired_entries"] = expired
	stats["active_entries"] = len(d.cache) - expired

	return stats
}
