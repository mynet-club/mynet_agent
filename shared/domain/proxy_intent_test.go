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

func TestEffectiveCloudLaterWins(t *testing.T) {
	now := time.Now()
	s := &ProxyIntentState{}
	if err := s.ApplyLocal(mustOKPolicy(), now); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyCloud(mustOKPolicy(), 20, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	eff := s.Effective()
	if eff == nil || eff.Source != ProxySourceCloud {
		t.Fatalf("云端后写应胜: %+v", eff)
	}
}

func TestEffectiveSameRevisionCloudWins(t *testing.T) {
	now := time.Now()
	s := &ProxyIntentState{}
	// 同 revision 1
	if err := s.ApplyLocal(mustOKPolicy(), now); err != nil {
		t.Fatal(err)
	}
	// 强行把 cloud 设成同 revision
	if err := s.ApplyCloud(mustOKPolicy(), 1, now); err != nil {
		t.Fatal(err)
	}
	// 本地 revision 被 ApplyLocal 设为 1；cloud 也是 1
	if s.Local.Revision != s.Cloud.Revision {
		t.Fatalf("测试前提应同 revision: local=%d cloud=%d", s.Local.Revision, s.Cloud.Revision)
	}
	eff := s.Effective()
	if eff == nil || eff.Source != ProxySourceCloud {
		t.Fatalf("同时刻应云优先: %+v", eff)
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
