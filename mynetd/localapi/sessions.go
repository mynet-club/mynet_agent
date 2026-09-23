package localapi

import (
	"sort"
	"sync"
)

// NodeSession — R5-1 多节点并行。
//
// 决策（2026-09-23 / long-range）：短期互斥 `nodes/switch`，并行作增量。
// 本文件把「当前单节点」升级为**进程级并行会话**：
//   - 多个 node_id 可同时 active
//   - UI 仍有焦点节点（focus），切换只改 focus 不踢其它
//   - 第一期只做会话表 + 焦点，驱动实例绑定后续接 A-3
type NodeSession struct {
	NodeID  string `json:"node_id"`
	Active  bool   `json:"active"`
	Focused bool   `json:"focused"`
	Driver  string `json:"driver,omitempty"` // gnb|wireguard|dual
}

type SessionTable struct {
	mu       sync.RWMutex
	sessions map[string]*NodeSession
	focus    string
}

func NewSessionTable() *SessionTable {
	return &SessionTable{sessions: map[string]*NodeSession{}}
}

// Attach 上线/注册一个节点会话（并行：不踢旧的）
func (t *SessionTable) Attach(nodeID, driver string) *NodeSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sessions[nodeID]
	if !ok {
		s = &NodeSession{NodeID: nodeID}
		t.sessions[nodeID] = s
	}
	s.Active = true
	s.Driver = driver
	if t.focus == "" {
		t.focus = nodeID
		s.Focused = true
	}
	return s
}

// Detach 下线
func (t *SessionTable) Detach(nodeID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.sessions, nodeID)
	if t.focus == nodeID {
		t.focus = ""
		for _, s := range t.sessions {
			if s.Active {
				t.focus = s.NodeID
				s.Focused = true
				break
			}
		}
	}
}

// Focus 切换 UI 焦点（R5-1：并行下 switch 只改焦点）
func (t *SessionTable) Focus(nodeID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sessions[nodeID]
	if !ok || !s.Active {
		return false
	}
	for _, x := range t.sessions {
		x.Focused = x.NodeID == nodeID
	}
	t.focus = nodeID
	return true
}

// List 稳定排序
func (t *SessionTable) List() []NodeSession {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]NodeSession, 0, len(t.sessions))
	for _, s := range t.sessions {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// Focused 当前焦点
func (t *SessionTable) Focused() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.focus
}

// ParallelCount 活跃并行数
func (t *SessionTable) ParallelCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	n := 0
	for _, s := range t.sessions {
		if s.Active {
			n++
		}
	}
	return n
}
