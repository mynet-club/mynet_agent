package mynet

import (
	"net"
	"testing"
)

func TestRouteHitsLocalSubnet(t *testing.T) {
	_, lan, _ := net.ParseCIDR("192.168.0.142/24")
	locals := []*net.IPNet{lan}

	if ln, hit := routeHitsLocalSubnet("192.168.0.0/24", locals); !hit || ln != "192.168.0.0/24" {
		t.Fatalf("同段应命中: hit=%v ln=%s", hit, ln)
	}
	if _, hit := routeHitsLocalSubnet("192.168.9.0/24", locals); hit {
		t.Fatal("不同段不应命中")
	}
	// 更宽的包含关系也算命中（10.0.0.0/8 覆盖本机 /24）
	_, wide, _ := net.ParseCIDR("10.182.0.0/16")
	if _, hit := routeHitsLocalSubnet("10.0.0.0/8", []*net.IPNet{wide}); !hit {
		t.Fatal("更宽网段应命中")
	}
}

func TestPlanSkipsLocalLanRoute(t *testing.T) {
	// 构造 bundle：对端宣告 192.168.0.0/24（本机也在该段）
	locals := localConnectedCIDRs()
	if len(locals) == 0 {
		t.Skip("无直连网段（CI）")
	}
	var target string
	for _, ln := range locals {
		if ones, _ := ln.Mask.Size(); ones == 24 {
			target = ln.IP.Mask(ln.Mask).String() + "/24"
			break
		}
	}
	if target == "" {
		t.Skip("本机无 /24 直连网段")
	}
	if _, hit := routeHitsLocalSubnet(target, locals); !hit {
		t.Fatalf("构造的本机网段 %s 应命中", target)
	}
}
