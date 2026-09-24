package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// redis 键（C-6）：
//
//	mynet:presence:<node>  HASH {online, verified, gw}
//	mynet:events:<node>    PUBLISH 一拍（有待投递命令）
const (
	presenceKeyPrefix = "mynet:presence:"
	eventsChannelPref = "mynet:events:"
)

// redisPresence 多实例 pub/sub。连接失败/命令失败时降级到内存（行为明确）。
type redisPresence struct {
	rdb      *redis.Client
	self     string // 本实例 ID，用于日志
	fallback *MemoryPresence
	enabled  bool
}

// NewRedisPresence 连接 Redis；addr 为空或 Ping 失败则返回 MemoryPresence。
func NewRedisPresence(addr, password string, db int) (PresenceStore, error) {
	mem := NewMemoryPresence()
	if addr == "" {
		return mem, nil
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		// 故障降级：Redis 不可用 → 单实例内存
		return mem, nil
	}
	return &redisPresence{
		rdb: rdb, self: fmt.Sprintf("gw-%d", time.Now().UnixNano()),
		fallback: mem, enabled: true,
	}, nil
}

func (p *redisPresence) key(nodeID string) string { return presenceKeyPrefix + nodeID }

func (p *redisPresence) SetOnline(ctx context.Context, nodeID string, verified bool) error {
	_ = p.fallback.SetOnline(ctx, nodeID, verified)
	if !p.enabled {
		return nil
	}
	err := p.rdb.HSet(ctx, p.key(nodeID), map[string]interface{}{
		"online":   1,
		"verified": boolToInt(verified),
		"gw":       p.self,
		"at":       time.Now().Unix(),
	}).Err()
	if err != nil {
		p.enabled = false // 降级
	}
	return nil
}

func (p *redisPresence) SetOffline(ctx context.Context, nodeID string) error {
	_ = p.fallback.SetOffline(ctx, nodeID)
	if !p.enabled {
		return nil
	}
	err := p.rdb.HSet(ctx, p.key(nodeID), map[string]interface{}{
		"online": 0, "gw": p.self, "at": time.Now().Unix(),
	}).Err()
	if err != nil {
		p.enabled = false
	}
	return nil
}

func (p *redisPresence) IsOnline(ctx context.Context, nodeID string) (bool, bool, error) {
	if !p.enabled {
		return p.fallback.IsOnline(ctx, nodeID)
	}
	m, err := p.rdb.HGetAll(ctx, p.key(nodeID)).Result()
	if err != nil {
		p.enabled = false
		return p.fallback.IsOnline(ctx, nodeID)
	}
	if len(m) == 0 {
		return false, false, nil
	}
	return m["online"] == "1", m["verified"] == "1", nil
}

func (p *redisPresence) Subscribe(ctx context.Context, nodeID string) (<-chan []byte, error) {
	// 本机事件走内存订阅；跨实例事件经 PUBLISH。两边合并到同一个 out。
	local, err := p.fallback.Subscribe(ctx, nodeID)
	if !p.enabled {
		return local, err
	}
	out := make(chan []byte, 1)
	go func() {
		defer close(out)
		sub := p.rdb.Subscribe(ctx, eventsChannelPref+nodeID)
		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				_ = sub.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				// msg.Payload 是发布方写的**帧原文**。原实现这里是 `_ = msg`
				// —— 只发一个空 struct 出去，接线后拿到也还原不出 event/data。
				sendByte(out, []byte(msg.Payload))
			case frame, ok := <-local:
				if !ok {
					return
				}
				sendByte(out, frame)
			}
		}
	}()
	return out, nil
}

func (p *redisPresence) Publish(ctx context.Context, nodeID string, frame []byte) error {
	_ = p.fallback.Publish(ctx, nodeID, frame)
	if !p.enabled {
		return nil
	}
	// 发**帧原文**，不是硬编码的 {"event":"queued"} —— 接收方要靠它还原
	// 要推给节点的完整内容。
	if err := p.rdb.Publish(ctx, eventsChannelPref+nodeID, frame).Err(); err != nil {
		p.enabled = false
	}
	return nil
}

// sendByte 非阻塞投递：缓冲满即丢。
// back 的 notify 有幂等重试语义，丢了会重发；阻塞则会卡住整条 notify 路径。
func sendByte(ch chan []byte, b []byte) {
	select {
	case ch <- b:
	default:
	}
}

func (p *redisPresence) Close() error {
	_ = p.fallback.Close()
	if p.rdb != nil {
		return p.rdb.Close()
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
