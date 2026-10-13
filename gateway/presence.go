package gateway

import (
	"context"
	"sync"
	"time"
)

// PresenceStore — C-6：多实例下的 presence / 推送路由。
//
// 内存实现是**单实例降级**（Redis 不可用时行为明确：退回本机表）。
// Redis 实现走 pub/sub：跨 gateway 实例同步 online/verified 状态，
// 并把**事件帧本身**投递给连在别的实例上的节点。
//
// ⚠️ Subscribe/Publish 必须带**帧内容**，不能只传 nodeID。
// 原接口两者的通道元素是 `struct{}`（Publish 还把 payload 硬编码成
// `{"event":"queued"}`、Subscribe 收到消息后 `_ = msg` 直接丢）——
// 也就是说跨实例只能传「有事」，传不了「什么事」。那样节点连 A 时，
// 后端通知 B，B 最多能喊一声，A 收到信号却拿不到 event/data，
// 除非再回头问后端一次（后端是发起方，没这接口）。
// 帧格式与 WS 下发帧一致（`{event, data}`），即 handleNotify 要推的原文。
type PresenceStore interface {
	SetOnline(ctx context.Context, nodeID string, verified bool) error
	SetOffline(ctx context.Context, nodeID string) error
	IsOnline(ctx context.Context, nodeID string) (online, verified bool, err error)
	// Subscribe 返回「为该节点投递的事件帧」。生命周期由 ctx 控制：cancel 即退订。
	Subscribe(ctx context.Context, nodeID string) (<-chan []byte, error)
	// Publish 把一帧事件投递给**其它** gateway 实例（同实例内直接投递由 Registry 负责）。
	Publish(ctx context.Context, nodeID string, frame []byte) error
	Close() error
}

// MemoryPresence 单实例降级（Redis 挂时自动退回）。
type MemoryPresence struct {
	mu   sync.RWMutex
	m    map[string]presenceEntry
	subs map[string][]chan []byte
}

type presenceEntry struct {
	online   bool
	verified bool
	at       time.Time
}

func NewMemoryPresence() *MemoryPresence {
	return &MemoryPresence{
		m:    map[string]presenceEntry{},
		subs: map[string][]chan []byte{},
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

func (p *MemoryPresence) Subscribe(_ context.Context, nodeID string) (<-chan []byte, error) {
	ch := make(chan []byte, 1)
	p.mu.Lock()
	p.subs[nodeID] = append(p.subs[nodeID], ch)
	p.mu.Unlock()
	return ch, nil
}

func (p *MemoryPresence) Publish(_ context.Context, nodeID string, frame []byte) error {
	p.mu.Lock()
	chs := p.subs[nodeID]
	p.mu.Unlock()
	for _, ch := range chs {
		select {
		case ch <- frame:
		default:
			// 缓冲满即丢：后端的 notify 是幂等重试语义，丢了会重发。
			// 阻塞才是不可接受的 —— 会让 Publish 卡住整个 notify 路径。
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
	p.subs = map[string][]chan []byte{}
	return nil
}
