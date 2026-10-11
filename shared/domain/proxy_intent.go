package domain

// Proxy 双控合并（本地 WebUI ↔ 云端 envelope）。
//
// D1 收窄后两轨**职责正交**，不再同型抢胜负：
//   - Cloud 轨 = 云端出口许可（enabled + acl，T11），云端权威
//   - Local 轨 = 客户端分流策略（role/region/split/exits/dns…），本机环境相关
//
// Effective() 按职责合成：client 侧字段全取 Local，server 侧（ACL/出口能力）取 Cloud。
//
// 本地写成功即先生效，并标 Dirty；回写成功才清 Dirty。
// Effective 是唯一生效出口——执行器只认它，不认 local/cloud 原始片段。

import (
	"fmt"
	"reflect"
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

// Effective 合成唯一生效策略：client 侧取 Local 轨，server 侧（出口许可）取 Cloud 轨。
// 两轨都空 / 合成后未启用 → nil（执行器据此清理）。
//
// 旧模型是「Local/Cloud 同型、按 revision 抢胜负」；D1 收窄后 Cloud 只带许可、
// Local 只带客户端策略，二者不再冲突，故改为按职责合成。
func (s *ProxyIntentState) Effective() *StampedProxyPolicy {
	if s == nil {
		return nil
	}
	l, c := s.Local, s.Cloud
	if l == nil && c == nil {
		return nil
	}
	var base StampedProxyPolicy
	switch {
	case l != nil:
		base = *l
	case c != nil:
		base = *c
	}
	// server 侧：合入云端出口许可（enabled + acl）
	if c != nil && c.Policy.Enabled {
		base.Policy.Enabled = true
		base.Policy.ACL = c.Policy.ACL
		// client 侧是否同时开，决定角色是 both 还是纯 server
		if l != nil && l.Policy.Enabled {
			base.Policy.Role = ProxyRoleBoth
		} else {
			base.Policy.Role = ProxyRoleServer
		}
	}
	if !base.Policy.Enabled {
		return nil
	}
	return &base
}

// ApplyLocal 本地写入：revision 自增（在当前最大上 +1），标 Dirty。
func (s *ProxyIntentState) ApplyLocal(p ProxyPolicy, now time.Time) error {
	// client 侧若未带 Split，由**此处**按 region×policy 解算（与 SetProxyEnabled 同口径）。
	// 上游（LuCI/桥）只该给业务参数 role/region/policy；矩阵解算是权威侧的事，
	// 免得每个调用方各抄一份 region×policy 矩阵（旧线正因此出过 domestic+blacklist 失效）。
	if p.IsClientLike() && p.Split == nil {
		src, mode, err := ResolveProxySplit(p.Region, p.Policy)
		if err != nil {
			return err
		}
		p.Split = &ProxySplit{ListSource: src, MatchMode: mode}
	}
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
//
// knownRevision > 0 时做**单调门禁**：
//   - 小于当前 Cloud.Revision → 拒绝（旧 generation 乱序/重放）；
//   - 等于当前且内容不同 → 拒绝（同代改写）；
//   - 等于当前且内容相同 → 幂等空操作；
//   - 大于当前 → 接受。
//
// knownRevision=0 保持兼容自增，不触发门禁。
func (s *ProxyIntentState) ApplyCloud(p ProxyPolicy, knownRevision int64, now time.Time) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if knownRevision > 0 && s.Cloud != nil {
		if knownRevision < s.Cloud.Revision {
			return fmt.Errorf("stale cloud revision %d < current %d，拒绝旧 generation 重放",
				knownRevision, s.Cloud.Revision)
		}
		if knownRevision == s.Cloud.Revision {
			if reflect.DeepEqual(s.Cloud.Policy, p) {
				return nil
			}
			return fmt.Errorf("cloud revision %d 内容不一致，拒绝同代改写", knownRevision)
		}
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
