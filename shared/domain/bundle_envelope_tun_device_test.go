package domain

import (
	"strings"
	"testing"
)

// gnbEnv 造一份「后端两处同源」的最小 envelope：node.interface 与
// gnb.options.ifname 是同一个值（2026-10-01 起的下发口径，第一步过渡期）。
func gnbEnv(authority, legacy string) *BundleEnvelope {
	env := &BundleEnvelope{
		Node: NodeRef{ID: "8283289760467957", Interface: authority},
		Gnb:  &GnbIntent{Identity: GnbIdentity{NodeID: "8283289760467957"}},
	}
	if legacy != "" {
		env.Gnb.Options = &GnbOptions{Ifname: legacy}
	}

	return env
}

// TestTunDeviceAuthority 权威是 node.interface。
//
// 现网事实：网关 182 的 `gnb_tun` 被另一个 zone 占用，本节点的隧道口必须是
// `gnb_tun_16`。后端两处同源发同一个值，agent 任何一处读到另一个字段就是漂移。
func TestTunDeviceAuthority(t *testing.T) {
	got, err := gnbEnv("gnb_tun_16", "gnb_tun_16").TunDevice()
	if err != nil {
		t.Fatal(err)
	}
	if got != "gnb_tun_16" {
		t.Fatalf("TunDevice()=%q，应为 node.interface 的值", got)
	}
}

// TestTunDeviceLegacyOnly 旧后端的 bundle（只有 gnb.options.ifname）过渡期仍可用。
//
// 这条是**发布顺序**的保险：agent 先升级、后端后停发别名，中间态不能让节点
// 突然解析不出接口名而停摆。后端停发之后（B 计划第二步）该分支才会成为死路。
func TestTunDeviceLegacyOnly(t *testing.T) {
	got, err := gnbEnv("", "gnb_tun_16").TunDevice()
	if err != nil {
		t.Fatal(err)
	}
	if got != "gnb_tun_16" {
		t.Fatalf("legacy 兼容路径=%q", got)
	}
}

// TestTunDeviceConflict 两处不一致 = 硬错误，不挑一个。
//
// 挑错一个的后果不是「起不来」而是「起来在别的口上」：`ip route … dev X` 只要
// X 存在就执行成功，流量于是搬进另一个 zone 的隧道。这种错必须响。
func TestTunDeviceConflict(t *testing.T) {
	_, err := gnbEnv("gnb_tun_16", "gnb_tun").TunDevice()
	if err == nil {
		t.Fatal("node.interface 与 gnb.options.ifname 冲突时必须报错")
	}
	if !strings.Contains(err.Error(), "冲突") {
		t.Fatalf("错误应说明是两字段冲突，实际：%v", err)
	}
}

// TestTunDeviceEmptyRefusesDriverDefault 两个字段都空 → 报错。
//
// 关键是**不许退回 gnb 自己的默认名**：默认名在单 zone 机器上能用，
// 在 182 这类机器上恰好是别人占用的接口，于是配置"成功"、流量走错隧道。
// 报错让这台停在上一版配置，运维能从 mynetd 日志看到为什么。
func TestTunDeviceEmptyRefusesDriverDefault(t *testing.T) {
	for _, name := range []string{"", "   "} {
		_, err := gnbEnv(name, "").TunDevice()
		if err == nil {
			t.Fatalf("接口名为空（%q）不该解析成功", name)
		}
	}
	// 连 gnb 段都没有（wireguard 节点）同样不猜：wg 的默认名 wg0 一样是猜出来的
	_, err := (&BundleEnvelope{Node: NodeRef{ID: "1"}}).TunDevice()
	if err == nil {
		t.Fatal("wireguard 段缺接口名时不该静默用 wg0")
	}
}

// TestTunDeviceIllegalName 超长与非法字符一律报错（长度/字符集见 ValidateTunDevice 注释）。
func TestTunDeviceIllegalName(t *testing.T) {
	bad := []string{
		"gnb_tun_123456789", // 17 字符：内核与 gnb 静默截断
		"gnb tun",           // 空格：ip route 拼接断词
		"gnb_tun;rm -rf /",  // 分号：注入
		"gnb/tun",           // 斜杠：非法接口名
		"gnb_tun_１６",        // 非 ASCII：strlen 按字节算，下游对不上
	}
	for _, b := range bad {
		if _, err := gnbEnv(b, "").TunDevice(); err == nil {
			t.Errorf("非法接口名 %q 竟解析成功", b)
		}
		// 权威非法时，legacy 即便合法也不能把它带过去
		if _, err := gnbEnv(b, "gnb_tun").TunDevice(); err == nil {
			t.Errorf("非法权威 %q 被 legacy %q 绕过", b, "gnb_tun")
		}
	}

	// 对照：合法形态（含后端声明允许的字符）
	for _, ok := range []string{"gnb_tun_16", "wg0", "utun3", "mynet0"} {
		if _, err := gnbEnv(ok, "").TunDevice(); err != nil {
			t.Errorf("合法接口名 %q 被拒: %v", ok, err)
		}
	}
}

// TestValidateTunDeviceLength 长度报错里必须带实际长度 —— 截断这类错只有
// 「17 > 15」这句话能定位到是谁把 node_id 拼进了接口名。
func TestValidateTunDeviceLength(t *testing.T) {
	err := ValidateTunDevice("gnb_tun_123456789")
	if err == nil || !strings.Contains(err.Error(), "17") {
		t.Fatalf("长度错误应带上实际字符数，实际：%v", err)
	}
	if err := ValidateTunDevice(strings.Repeat("a", TunDeviceMaxLen)); err != nil {
		t.Fatalf("恰好 15 字符应合法: %v", err)
	}
}

// TestDeviceForRoute OS 路由的 dev 必须指向本节点隧道口。
//
// 这是 182 失联的直接形态：bundle 把 dev 写成 `gnb_tun`（另一个 zone 的接口），
// 命令执行成功、路由"装上了"，对端 /32 却进了别人的隧道 —— 症状是 ping 不通、
// 而后端侧日志一切正常。dev 为空不在此列（那是走网关/别的表的路由）。
func TestDeviceForRoute(t *testing.T) {
	env := gnbEnv("gnb_tun_16", "gnb_tun_16")

	if err := env.DeviceForRoute("gnb_tun_16"); err != nil {
		t.Fatalf("同源 dev 应通过: %v", err)
	}
	if err := env.DeviceForRoute(""); err != nil {
		t.Fatalf("空 dev（网关路由）不该被拒: %v", err)
	}
	err := env.DeviceForRoute("gnb_tun")
	if err == nil {
		t.Fatal("dev 指向别的接口必须拒绝，否则路由装到别人隧道上")
	}
	if !strings.Contains(err.Error(), "gnb_tun_16") {
		t.Fatalf("错误里要给出期望的接口名，实际：%v", err)
	}

	// 接口名本身解析不出来时，路由校验也必须失败（不许带着错名字继续）
	if err := (&BundleEnvelope{Node: NodeRef{ID: "1"}}).DeviceForRoute("gnb_tun"); err == nil {
		t.Fatal("接口名缺失时不该校验通过")
	}
}
