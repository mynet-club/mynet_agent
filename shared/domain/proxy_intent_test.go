package domain

import (
	"errors"
	"testing"
	"time"
)

func mustOKPolicy() ProxyPolicy {
	return ProxyPolicy{
		Enabled: true,
		Role:    "client",
		Region:  ProxyRegionDomestic,
		Policy:  ProxyPolicyWhitelist,
		Split: &ProxySplit{
			ListSource: ProxyListInternational,
			MatchMode:  ProxyMatchNormal,
		},
		Exits: []ProxyExit{{NodeID: "9001", Enabled: true}},
	}
}

func TestEffectiveEmpty(t *testing.T) {
	s := &ProxyIntentState{}
	if s.Effective() != nil {
		t.Fatal("空状态应无 effective")
	}
}

func TestEffectiveLocalLaterWins(t *testing.T) {
	now := time.Now()
	s := &ProxyIntentState{}
	if err := s.ApplyCloud(mustOKPolicy(), 5, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyLocal(mustOKPolicy(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	eff := s.Effective()
	if eff == nil || eff.Source != ProxySourceLocal {
		t.Fatalf("本地后写应胜: %+v", eff)
	}
	if !s.Status.Dirty {
		t.Fatal("本地写后必须 Dirty")
	}
	if s.Status.PendingRevision != 6 {
		t.Fatalf("pending=%d", s.Status.PendingRevision)
	}
}

// D1 收窄后 Cloud 轨只带出口许可：与 Local 客户端策略**正交合成**，不再按 revision 抢胜负。
func TestEffectiveComposesLocalClientWithCloudPermission(t *testing.T) {
	now := time.Now()
	s := &ProxyIntentState{}
	if err := s.ApplyLocal(mustOKPolicy(), now); err != nil {
		t.Fatal(err)
	}
	perm := ProxyPolicy{
		Enabled: true,
		Role:    ProxyRoleServer,
		ACL:     &ProxyACL{Mode: ProxyACLAllowlist, AllowedNodeIDs: []string{"9001"}},
	}
	if err := s.ApplyCloud(perm, 20, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	eff := s.Effective()
	if eff == nil {
		t.Fatal("应合成出 effective")
	}
	// client 侧字段来自 Local
	if eff.Policy.Region != ProxyRegionDomestic || eff.Policy.Split == nil || len(eff.Policy.Exits) != 1 {
		t.Fatalf("client 侧字段应来自 Local: %+v", eff.Policy)
	}
	// server 侧（ACL）来自 Cloud 许可，角色合成为 both
	if eff.Policy.ACL == nil || eff.Policy.Role != ProxyRoleBoth {
		t.Fatalf("server 许可应合入且 role=both: %+v", eff.Policy)
	}
}

// 仅云端许可、无 Local：纯 server 侧，不装 client 策略路由。
func TestEffectiveCloudPermissionOnlyIsServer(t *testing.T) {
	s := &ProxyIntentState{}
	if err := s.ApplyCloud(ProxyPolicy{Enabled: true, Role: ProxyRoleServer}, 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	eff := s.Effective()
	if eff == nil || !eff.Policy.IsServerLike() || eff.Policy.IsClientLike() {
		t.Fatalf("仅许可应 server-like 且非 client-like: %+v", eff)
	}
}

func TestWritebackOKClearsDirtyAndAligns(t *testing.T) {
	now := time.Now()
	s := &ProxyIntentState{}
	if err := s.ApplyLocal(mustOKPolicy(), now); err != nil {
		t.Fatal(err)
	}
	if !s.Status.Dirty {
		t.Fatal("应 Dirty")
	}
	s.MarkWritebackOK(10, now.Add(time.Second))
	if s.Status.Dirty {
		t.Fatal("回写成功应清 Dirty")
	}
	if s.Status.LastError != "" {
		t.Fatal("成功应清 LastError")
	}
	if s.Cloud == nil || s.Cloud.Revision != 10 || s.Local.Revision != 10 {
		t.Fatalf("revision 应对齐 10: local=%d cloud=%d", s.Local.Revision, s.Cloud.Revision)
	}
	// 对齐后 effective 仍是本地那份意图（内容），来源因云优先变成 cloud——内容一致即可
	eff := s.Effective()
	if eff == nil {
		t.Fatal("应有 effective")
	}
}

func TestWritebackFailKeepsDirty(t *testing.T) {
	now := time.Now()
	s := &ProxyIntentState{}
	if err := s.ApplyLocal(mustOKPolicy(), now); err != nil {
		t.Fatal(err)
	}
	s.MarkWritebackFail(errors.New("gateway 不可达"), now.Add(time.Second))
	if !s.Status.Dirty {
		t.Fatal("失败必须保持 Dirty")
	}
	if s.Status.LastError != "gateway 不可达" {
		t.Fatalf("LastError=%q", s.Status.LastError)
	}
	if eff := s.Effective(); eff == nil || eff.Source != ProxySourceLocal {
		t.Fatalf("失败后 effective 仍应本地: %+v", eff)
	}
}

func TestApplyLocalRejectsInvalid(t *testing.T) {
	s := &ProxyIntentState{}
	err := s.ApplyLocal(ProxyPolicy{Enabled: true, Role: "client"}, time.Now())
	if err == nil {
		t.Fatal("client 缺 region/policy/exits 应拒")
	}
	if s.Local != nil || s.Status.Dirty {
		t.Fatal("非法写入不得改动状态")
	}
}

func TestDescribe(t *testing.T) {
	now := time.Now()
	s := &ProxyIntentState{}
	if err := s.ApplyLocal(mustOKPolicy(), now); err != nil {
		t.Fatal(err)
	}
	s.MarkWritebackFail(errors.New("x"), now)
	got := s.Describe()
	if got == "" {
		t.Fatal("Describe 为空")
	}
}

// 旧 generation 重放：knownRevision 不大于当前 Cloud.Revision 时必须拒绝，
// 保留已有许可，不得被乱序/重复推送覆盖。
func TestApplyCloudRejectsStaleReplay(t *testing.T) {
	now := time.Now()
	s := &ProxyIntentState{}
	newer := ProxyPolicy{Enabled: true, Role: ProxyRoleServer}
	if err := s.ApplyCloud(newer, 200, now); err != nil {
		t.Fatal(err)
	}
	stale := ProxyPolicy{Enabled: true, Role: ProxyRoleServer,
		ACL: &ProxyACL{Mode: ProxyACLAllowlist, AllowedNodeIDs: []string{"1"}}}
	if err := s.ApplyCloud(stale, 100, now.Add(time.Second)); err == nil {
		t.Fatal("旧 revision 重放应拒绝")
	}
	if s.Cloud == nil || s.Cloud.Revision != 200 || s.Cloud.Policy.ACL != nil {
		t.Fatalf("重放不得覆盖 Cloud 轨: %+v", s.Cloud)
	}
	// 同 revision 内容不同也拒（防同代改写）
	if err := s.ApplyCloud(stale, 200, now.Add(2*time.Second)); err == nil {
		t.Fatal("同 revision 改写应拒绝")
	}
	// 更新的 revision 放行
	fresh := ProxyPolicy{Enabled: true, Role: ProxyRoleServer,
		ACL: &ProxyACL{Mode: ProxyACLAllowlist, AllowedNodeIDs: []string{"2"}}}
	if err := s.ApplyCloud(fresh, 300, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if s.Cloud.Revision != 300 || s.Cloud.Policy.ACL == nil {
		t.Fatalf("新 revision 应生效: %+v", s.Cloud)
	}
}

// knownRevision=0 保持兼容自增，不触发单调门禁。
func TestApplyCloudZeroRevisionStillAutoIncrements(t *testing.T) {
	now := time.Now()
	s := &ProxyIntentState{}
	if err := s.ApplyCloud(ProxyPolicy{Enabled: true, Role: ProxyRoleServer}, 50, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyCloud(ProxyPolicy{Enabled: true, Role: ProxyRoleServer}, 0, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if s.Cloud.Revision <= 50 {
		t.Fatalf("0 应自增超过当前: %d", s.Cloud.Revision)
	}
}

// GenerationRevision：真实 publisher 格式 `rev-{YmdHis}-{nodeId}-{seq}` 必须可比较，
// 否则 ParseInt 失败、旧 generation 会被当成最新。
func TestGenerationRevision(t *testing.T) {
	older := GenerationRevision("rev-20261008120000-2000000000000001-001")
	newer := GenerationRevision("rev-20261008120001-2000000000000001-001")
	if older <= 0 || newer <= older {
		t.Fatalf("generation 应单调: older=%d newer=%d", older, newer)
	}
	sameSec := GenerationRevision("rev-20261008120000-2000000000000001-002")
	if sameSec <= older {
		t.Fatalf("同秒 seq 递增应更大: %d vs %d", sameSec, older)
	}
	if GenerationRevision("7") != 7 {
		t.Fatal("纯整数 generation 原样返回")
	}
	if GenerationRevision("") != 0 || GenerationRevision("garbage") != 0 {
		t.Fatal("无法解析应返回 0")
	}
}

// ApplyLocal 自行解算 split：上游只给 role/region/policy 也生效（矩阵解算归权威侧）。
func TestApplyLocalResolvesSplit(t *testing.T) {
	s := &ProxyIntentState{}
	p := ProxyPolicy{Enabled: true, Role: ProxyRoleClient, Region: ProxyRegionDomestic, Policy: ProxyPolicyWhitelist}
	if err := s.ApplyLocal(p, time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.Local == nil || s.Local.Policy.Split == nil {
		t.Fatal("应解算出 split")
	}
	if s.Local.Policy.Split.ListSource != ProxyListInternational || s.Local.Policy.Split.MatchMode != ProxyMatchNormal {
		t.Fatalf("split 解算错: %+v", s.Local.Policy.Split)
	}
	// 非法 region/policy 不再硬拒（2026-10-17 用户裁决）：钳到合法默认后继续，
	// 否则 UI 串值（region=dns policy=off）会卡死保存。
	var s2 ProxyIntentState
	if err := s2.ApplyLocal(
		ProxyPolicy{Enabled: true, Role: ProxyRoleClient, Region: "dns", Policy: "off"}, time.Now()); err != nil {
		t.Fatalf("非法 region/policy 应钳到默认，不报错: %v", err)
	}
	if s2.Local == nil {
		t.Fatal("应写入 Local")
	}
	if s2.Local.Policy.Region != ProxyRegionDomestic || s2.Local.Policy.Policy != ProxyPolicyWhitelist {
		t.Fatalf("应钳到 domestic/whitelist，got %s/%s", s2.Local.Policy.Region, s2.Local.Policy.Policy)
	}
	if s2.Local.Policy.Split == nil || s2.Local.Policy.Split.ListSource != ProxyListInternational {
		t.Fatalf("钳后应解算出 split，got %+v", s2.Local.Policy.Split)
	}
}
