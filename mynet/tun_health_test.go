package mynet

import (
	"strings"
	"testing"
	"time"
)

// G3 缺口⑥：隧道健康判据（tun 在/不在两态）+ 启动验收 fail loud。
// 现象（180/183 实证）：tun 名被占/创建失败时 gnb 进程活着但零流量，
// 服务 "active"、状态面无任何信号 —— 判据必须把「tun 在不在、IP 挂没挂」说出来。

// 健康判据两态：VPN IP 挂上了（tun 在）→ 健康；挂不上（tun 不在）→ 不健康。
func TestComputeTunHealthTwoStates(t *testing.T) {
	up := computeTunHealth("10.1.0.2", "gnb_tun",
		func(ip string) string { return "utun5" },
		func(name string) bool { return true })
	if !up.Checked || !up.VpnIPAttached || !up.TunExists || !up.Healthy() {
		t.Fatalf("tun 在应判健康: %+v", up)
	}
	if up.TunIface != "utun5" {
		t.Fatalf("挂 IP 的接口应报出来: %+v", up)
	}

	down := computeTunHealth("10.1.0.2", "gnb_tun",
		func(ip string) string { return "" },
		func(name string) bool { return false })
	if !down.Checked || down.VpnIPAttached || down.TunExists || down.Healthy() {
		t.Fatalf("tun 不在应判不健康: %+v", down)
	}
}

// 无判据（VPN IP 与期望口名都未知）不许瞎判。
func TestComputeTunHealthUnchecked(t *testing.T) {
	h := computeTunHealth("", "", func(string) string { return "" }, nil)
	if h.Checked || h.Healthy() {
		t.Fatalf("无判据应 Checked=false: %+v", h)
	}
}

// 期望口在、IP 没挂（名字被占的形态）：IP 判据说了算 ——
// 名字在 ≠ 隧道可用；darwin 上配置名（gnb_tun）与真实口（utunN）本就不同名。
func TestComputeTunHealthNameExistsButIPNotAttached(t *testing.T) {
	h := computeTunHealth("10.1.0.2", "gnb_tun",
		func(string) string { return "" },
		func(string) bool { return true })
	if !h.TunExists || h.VpnIPAttached || h.Healthy() {
		t.Fatalf("名字在但 IP 没挂 = 零流量，必须判不健康: %+v", h)
	}
}

// 启动验收 fail loud：gnb 活了但 tun 未起 → 明确报「在跑但 tun 未起」+ 归因提示。
func TestTunStartAcceptanceFailLoud(t *testing.T) {
	down := computeTunHealth("10.1.0.2", "gnb_tun", func(string) string { return "" }, nil)
	err := tunStartAcceptance(down, "gnb")
	if err == nil {
		t.Fatal("tun 未起必须 fail loud")
	}
	for _, want := range []string{"gnb 在跑但 tun 未起", "疑名字被占/权限不足", "10.1.0.2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误信息缺 %q: %v", want, err)
		}
	}

	up := computeTunHealth("10.1.0.2", "gnb_tun", func(string) string { return "utun5" }, nil)
	if err := tunStartAcceptance(up, "gnb"); err != nil {
		t.Fatalf("tun 在不该拦: %v", err)
	}

	// 无判据不拦（route.conf/意图都没有 self 行时无法验证，不能误杀启动）
	none := computeTunHealth("", "", nil, nil)
	if err := tunStartAcceptance(none, "gnb"); err != nil {
		t.Fatalf("无判据不该拦: %v", err)
	}
}

// verifyTunAfterStart：短窗内收敛即过；一直不收敛才 fail loud（非零退出的判据源）。
func TestVerifyTunAfterStartRetriesThenFailsLoud(t *testing.T) {
	calls := 0
	probe := func() TunHealth {
		calls++
		if calls < 3 {
			return computeTunHealth("10.1.0.2", "gnb_tun", func(string) string { return "" }, nil)
		}
		return computeTunHealth("10.1.0.2", "gnb_tun", func(string) string { return "utun5" }, nil)
	}
	if err := verifyTunAfterStart("gnb", probe, 3, time.Millisecond); err != nil {
		t.Fatalf("第 3 次收敛应过: %v", err)
	}
	if calls != 3 {
		t.Fatalf("应重试到第 3 次: calls=%d", calls)
	}

	always := func() TunHealth {
		return computeTunHealth("10.1.0.2", "gnb_tun", func(string) string { return "" }, nil)
	}
	if err := verifyTunAfterStart("gnb", always, 2, time.Millisecond); err == nil {
		t.Fatal("一直不收敛必须 fail loud")
	}
}
