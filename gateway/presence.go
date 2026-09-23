package gateway

import (
	"context"
	"sync"
	"time"
)

// PresenceStore — C-6：多实例下的 presence / 推送路由。
//
// 内存实现是**单实例降级**（Redis 不可用时行为明确：退回本机表）。
// Redis 实现走 pub/sub：跨 gateway 实例同步 online/verified 状态。
type PresenceStore interface {
	SetOnline(ctx context.Context, nodeID string, verified bool) error
	SetOffline(ctx context.Context, nodeID string) error
	IsOnline(ctx context.Context, nodeID string) (online, verified bool, err error)
	// Subscribe 订阅「节点有事件」；cancel 退订
	Subscribe(ctx context.Context, nodeID string) (<-chan struct{}, error)
	// Publish 通知其它实例：该节点有待投递事件
	Publish(ctx context.Context, nodeID string) error
	Close() error
}

// MemoryPresence 单实例降级（Redis 挂时自动退回）。
type MemoryPresence struct {
	mu   sync.RWMutex
	m    map[string]presenceEntry
	subs map[string][]chan struct{}
}

type presenceEntry struct {
	online   bool
	verified bool
	at       time.Time
}

func NewMemoryPresence() *MemoryPresence {
	return &MemoryPresence{
		m:    map[string]presenceEntry{},
		subs: map[string][]chan struct{}{},
	}
}

func (p *MemoryPresence) SetOnline(_ context.Context, nodeID string, verified bool) error {
	p.mu.Lock()
	p.m[nodeID] = presenceEntry{online: true, verified: verified, at: time.Now()}
	p.mu.Unlock()
	return nil
}

func (p *MemoryPresence) SetOffline(_ context.Context, nodeID string) error {
	p.mu.Lock()
	p.m[nodeID] = presenceEntry{online: false, at: time.Now()}
	p.mu.Unlock()
	return nil
}

func (p *MemoryPresence) IsOnline(_ context.Context, nodeID string) (bool, bool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.m[nodeID]
	return ok && e.online, e.verified, nil
}

func (p *MemoryPresence) Subscribe(_ context.Context, nodeID string) (<-chan struct{}, error) {
	ch := make(chan struct{}, 1)
	p.mu.Lock()
	p.subs[nodeID] = append(p.subs[nodeID], ch)
	p.mu.Unlock()
	return ch, nil
}

func (p *MemoryPresence) Publish(_ context.Context, nodeID string) error {
	p.mu.Lock()
	chs := p.subs[nodeID]
	p.mu.Unlock()
	for _, ch := range chs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return nil
}

func (p *MemoryPresence) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, chs := range p.subs {
		for _, ch := range chs {
			close(ch)
		}
	}
	p.subs = map[string][]chan struct{}{}
	return nil
}
