package mynet

import (
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// planEnv 造一份「back 两处同源」的最小 gnb envelope，只带一条对端 /32 路由。
func planEnv(authority, routeDev string) *domain.BundleEnvelope {
	return &domain.BundleEnvelope{
		Node:   domain.NodeRef{ID: "8283289760467957", Interface: authority},
		Driver: domain.DriverTypeGNB,
		Network: domain.NetworkIntent{
			Addresses: []string{"10.182.236.182/24"},
			Routes: []domain.OsRoute{
				{Prefix: "10.182.236.184/32", Dev: routeDev},
			},
		},
		Gnb: &domain.GnbIntent{
			Identity: domain.GnbIdentity{NodeID: "8283289760467957"},
			Options:  &domain.GnbOptions{Ifname: authority},
		},
	}
}

// TestPlanFromBundleRejectsForeignDeviceRoute 计划的 dev 必须就是本机隧道口。
//
// 现网形态（网关 182 失联）：bundle 把 dev 写成 `gnb_tun`，而本机隧道口是
// `gnb_tun_16`（`gnb_tun` 属于另一个 zone）。`ip route replace … dev gnb_tun`
// **执行成功**，路由于是挂到别人的接口上，back 侧日志一切正常。
// 拒掉整份计划（返回错误、一条都不装）而不是跳过那一条：半装的状态更难排查。
func TestPlanFromBundleRejectsForeignDeviceRoute(t *testing.T) {
	_, err := planFromBundle(planEnv("gnb_tun_16", "gnb_tun"))
	if err == nil {
		t.Fatal("dev 不是本机隧道口时必须拒绝整份计划")
	}
	if !strings.Contains(err.Error(), "gnb_tun_16") {
		t.Fatalf("错误里应给出期望接口名: %v", err)
	}
	if !strings.Contains(err.Error(), "10.182.236.184/32") {
		t.Fatalf("错误里应带上出问题的那条前缀，便于定位: %v", err)
	}
}

// TestPlanFromBundleKeepsDeclaredDevice 同名时**原样保留** dev，不许"顺手"改名。
//
// 这里刻意不调 resolveDevice：那个函数在接口不存在时按本机 overlay IP 反查真实
// 名字（darwin 的 utunN / windows 的 P2PNet），那是**执行期**的事。计划期若允许
// 改名，就等于允许把路由搬到任何名字上 —— 校验会因此被绕过。
func TestPlanFromBundleKeepsDeclaredDevice(t *testing.T) {
	plan, err := planFromBundle(planEnv("gnb_tun_16", "gnb_tun_16"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Routes) != 1 {
		t.Fatalf("routes=%d", len(plan.Routes))
	}
	if plan.Routes[0].Device != "gnb_tun_16" {
		t.Fatalf("计划期不该改写接口名: %q", plan.Routes[0].Device)
	}
}

// TestPlanFromBundleProxyUsesAuthority proxy 的策略路由同样只用 node.interface。
//
// proxy 段用 TunDevice 做 iif 匹配与默认路由（proxy_apply.go），名字错就是
// 「分流看着生效、包从物理口出去」。与 routes[].dev 同源，不能各读各的字段。
func TestPlanFromBundleProxyUsesAuthority(t *testing.T) {
	env := planEnv("gnb_tun_16", "gnb_tun_16")
	env.Network.Proxy = mustPolicy(t, "domestic", "whitelist")

	plan, err := planFromBundle(env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Proxy == nil {
		t.Fatal("proxy 计划未生成")
	}
	if plan.Proxy.Runtime.TunDevice != "gnb_tun_16" {
		t.Fatalf("proxy TunDevice=%q，应取 node.interface", plan.Proxy.Runtime.TunDevice)
	}

	// 接口名解析不出来 → 连 proxy 计划也不许生成（宁可整个不装）
	env.Node.Interface = ""
	env.Gnb.Options.Ifname = ""
	if _, err := planFromBundle(env); err == nil {
		t.Fatal("无接口名时不该出计划")
	}
}
