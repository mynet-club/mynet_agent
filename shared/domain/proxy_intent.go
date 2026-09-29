package domain

// Proxy 双控合并（本地 WebUI ↔ 云端 envelope）。
//
// 规则：
//   1. 后覆盖先：比 Revision（generation/单调递增），大者胜
//   2. Revision 相同时刻 → 云优先（防离线老页面冲掉紧急策略）
//   3. 本地写成功即先生效，并标 Dirty；回写成功才清 Dirty
//   4. Effective 是唯一生效出口——执行器只认它，不认 local/cloud 原始片段
//
// 回写的是业务意图（region/policy/exits/dns…），不是 L6 投影（fwmark/nft）。

import (
	"fmt"
	"time"
)

// ProxySource 意图来源。
type ProxySource string

const (
	ProxySourceLocal ProxySource = "local"
	ProxySourceCloud ProxySource = "cloud"
)

// StampedProxyPolicy 带戳的代理意图。
type StampedProxyPolicy struct {
	Source ProxySource `json:"source"`
	// Revision 单调递增（云端 envelope.generation 可映射进来；本地每次写 +1）。
	Revision int64 `json:"revision"`
	// UpdatedAt 写入时刻（UTC）。Revision 相同用于审计，不参与胜负。
	UpdatedAt time.Time   `json:"updated_at"`
	Policy    ProxyPolicy `json:"policy"`
}

// ProxyWriteback 回写状态。
type ProxyWriteback struct {
	// Dirty 本地有未回写变更。
	Dirty bool `json:"dirty"`
	// LastAttempt 最近一次回写尝试。
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	// LastError 最近失败原因（成功后清空）。
	LastError string `json:"last_error,omitempty"`
	// PendingRevision 待回写的本地 revision。
	PendingRevision int64 `json:"pending_revision,omitempty"`
}

// ProxyIntentState 双控状态机的全部现场。
type ProxyIntentState struct {
	Local  *StampedProxyPolicy `json:"local,omitempty"`
	Cloud  *StampedProxyPolicy `json:"cloud,omitempty"`
	Status ProxyWriteback      `json:"status"`
}

// Effective 合并出唯一生效意图。两边都空 → nil。
//
// 后覆盖先：Revision 大者胜；Revision 相等时云优先。
func (s *ProxyIntentState) Effective() *StampedProxyPolicy {
	if s == nil {
		return nil
	}
	l, c := s.Local, s.Cloud
	switch {
	case l == nil && c == nil:
		return nil
	case l == nil:
		return c
	case c == nil:
		return l
	case l.Revision > c.Revision:
		return l
	case c.Revision >= l.Revision:
		// 含相等：云优先
		return c
	}
	return c
}

// ApplyLocal 本地写入：revision 自增（在当前最大上 +1），标 Dirty。
func (s *ProxyIntentState) ApplyLocal(p ProxyPolicy, now time.Time) error {
	if err := p.Validate(); err != nil {
		return err
	}
	next := int64(1)
	if s.Local != nil && s.Local.Revision >= next {
		next = s.Local.Revision + 1
	}
	if s.Cloud != nil && s.Cloud.Revision >= next {
		next = s.Cloud.Revision + 1
	}
	s.Local = &StampedProxyPolicy{
		Source:    ProxySourceLocal,
		Revision:  next,
		UpdatedAt: now.UTC(),
		Policy:    p,
	}
	s.Status.Dirty = true
	s.Status.PendingRevision = next
	return nil
}

// ApplyCloud 云端写入。已知云端 generation 时用 knownRevision；0 则自增。
// 云端写入不标 Dirty（它就是权威同步）。
func (s *ProxyIntentState) ApplyCloud(p ProxyPolicy, knownRevision int64, now time.Time) error {
	if err := p.Validate(); err != nil {
		return err
	}
	rev := knownRevision
	if rev <= 0 {
		rev = 1
		if s.Cloud != nil && s.Cloud.Revision >= rev {
			rev = s.Cloud.Revision + 1
		}
		if s.Local != nil && s.Local.Revision >= rev {
			rev = s.Local.Revision + 1
		}
	}
	s.Cloud = &StampedProxyPolicy{
		Source:    ProxySourceCloud,
		Revision:  rev,
		UpdatedAt: now.UTC(),
		Policy:    p,
	}
	return nil
}

// MarkWritebackOK 回写成功：清 Dirty，对齐本地 revision 到云端。
func (s *ProxyIntentState) MarkWritebackOK(cloudRevision int64, now time.Time) {
	s.Status.Dirty = false
	s.Status.LastError = ""
	s.Status.LastAttempt = now.UTC()
	s.Status.PendingRevision = 0
	if s.Local != nil && cloudRevision > 0 {
		// 回写成功后云端持有同一意图，revision 对齐避免下次误判「本地更新」
		s.Cloud = &StampedProxyPolicy{
			Source:    ProxySourceCloud,
			Revision:  cloudRevision,
			UpdatedAt: now.UTC(),
			Policy:    s.Local.Policy,
		}
		s.Local.Revision = cloudRevision
	}
}

// MarkWritebackFail 回写失败：保持 Dirty，记录原因。
func (s *ProxyIntentState) MarkWritebackFail(err error, now time.Time) {
	s.Status.Dirty = true
	s.Status.LastAttempt = now.UTC()
	s.Status.LastError = ""
	if err != nil {
		s.Status.LastError = err.Error()
	}
}

// Describe 一行状态，给 localapi / WebUI 用。
func (s *ProxyIntentState) Describe() string {
	if s == nil {
		return "empty"
	}
	eff := s.Effective()
	src := "none"
	rev := int64(0)
	if eff != nil {
		src = string(eff.Source)
		rev = eff.Revision
	}
	dirty := "clean"
	if s.Status.Dirty {
		dirty = "dirty"
		if s.Status.LastError != "" {
			dirty = "dirty(" + s.Status.LastError + ")"
		}
	}
	return fmt.Sprintf("effective=%s@rev%d %s", src, rev, dirty)
}
