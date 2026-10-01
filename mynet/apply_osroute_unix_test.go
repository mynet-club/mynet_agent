//go:build !windows

package mynet

import (
	"net"
	"strings"
	"testing"
)

// procRouteFixture 现网取证：.182（192.168.0.2，公司网关）2026-10-01 的
// /proc/net/route。注意它**没有** 192.168.100.0/24，而默认路由走 br-lan ——
// 旧实现用 `ip route get` 判所有权，于是那条站点路由被当成「别人占着」，
// apply 永久跳过重装。
const procRouteFixture = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
br-lan	00000000	0100A8C0	0003	0	0	0	00000000	0	0	0
gnb_tun_16	1600000A	00000000	0005	0	0	0	FFFFFFFF	0	0	0
WG01	0000020A	00000000	0001	0	0	0	00FFFFFF	0	0	0
gnb_tun_16	B8ECB60A	00000000	0005	0	0	0	FFFFFFFF	0	0	0
gnb_tun_16	B3ECB60A	00000000	0005	0	0	0	FFFFFFFF	0	0	0
br-lan	0000A8C0	00000000	0001	0	0	0	00FFFFFF	0	0	0
`

func TestProcRouteOwnerExactPrefix(t *testing.T) {
	cases := []struct {
		dest string
		want string
		why  string
	}{
		{"10.182.236.184", "gnb_tun_16", "裸 IP 按 /32 查"},
		{"10.182.236.184/32", "gnb_tun_16", "与 /32 写法同结果"},
		{"10.0.0.22/32", "gnb_tun_16", "小端 hex 解码正确"},
		{"10.2.0.0/24", "WG01", "覆盖它的 WG 路由算 owner"},
		{"192.168.0.0/24", "br-lan", "直连网段 = 别人占着，不碰"},
		{"192.168.100.0/24", "", "只有默认路由接住 ≠ 有人占有（.182 的卡死点）"},
		{"10.182.236.0/24", "", "更宽的段没有本尊条目，只有 /32"},
		{"10.182.236.99", "", "同段别的 /32 不存在 = 空闲"},
		{"fd00::1/128", "", "非 IPv4 不匹配"},
		{"not-an-ip", "", "脏输入"},
	}
	for _, c := range cases {
		if got := procRouteOwner(procRouteFixture, parseDestNet(c.dest)); got != c.want {
			t.Fatalf("procRouteOwner(%q)=%q want %q（%s）", c.dest, got, c.want, c.why)
		}
	}
}

// 旧行为的反证：这些目的地在 .182 上都会被 `ip route get` 回一个 br-lan，
// 当成所有权就让 apply 什么都装不上。
func TestProcRouteOwnerIgnoresDefaultRoute(t *testing.T) {
	_, want, err := net.ParseCIDR("192.168.100.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if dev := procRouteOwner(procRouteFixture, want); dev != "" {
		t.Fatalf("默认路由不该算 owner，got %q", dev)
	}
	// 只有显式装了 0.0.0.0/0 的查询才承认那条默认路由本身
	if dev := procRouteOwner(procRouteFixture, parseDestNet("0.0.0.0/0")); dev != "br-lan" {
		t.Fatalf("默认路由本尊应命中 br-lan，got %q", dev)
	}
}

func TestUnhexLE(t *testing.T) {
	// 10.182.236.184 → 0A B6 EC B8 → 内核按小端 u32 打成 B8ECB60A
	if got := unhexLE("B8ECB60A"); !net.IP(got).Equal(net.ParseIP("10.182.236.184")) {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"", "ZZ", "00", "0000000000"} {
		if unhexLE(bad) != nil {
			t.Fatalf("unhexLE(%q) 应为 nil", bad)
		}
	}
}

// BSD 侧：route(8) 查询会由覆盖路由（默认路由 / 直连口）替答，
// 必须核到 destination+mask 与本尊相等才算占有。
func TestBSDExactOwner(t *testing.T) {
	hostOut := `   route to: 10.182.236.184
destination: 10.182.236.184
  interface: utun6
      flags: <UP,HOST,DONE,STATIC>
 recvpipe  sendpipe  ssthresh  rtt,msec    rttvar  hopcount      mtu     expire
       0         0         0         0         0         0      1450         0
`
	netOut := `   route to: 192.168.100.0
destination: 192.168.100.0
       mask: 255.255.255.0
  interface: utun6
`
	// macOS 实测：目的地只被默认路由接住时打 destination: default / mask: default
	defaultOut := `   route to: 192.168.100.1
destination: default
       mask: default
    gateway: 192.168.0.1
  interface: en0
`
	cases := []struct {
		out  string
		dest string
		want string
		why  string
	}{
		{hostOut, "10.182.236.184", "utun6", "主机路由没有 mask 行，隐含 /32"},
		{hostOut, "10.182.236.184/32", "utun6", "等价写法"},
		{hostOut, "10.182.236.0/24", "", "别的前缀不算本尊"},
		{netOut, "192.168.100.0/24", "utun6", "网段精确匹配"},
		{netOut, "192.168.100.1", "", "只有 /24 时 /32 无本尊条目 = 空闲"},
		{defaultOut, "192.168.100.0/24", "", "默认路由替答 ≠ 有人占有"},
		{"", "192.168.100.0/24", "", "busybox route 不认 -n get，输出为空"},
		{hostOut, "not-an-ip", "", "脏输入"},
	}
	for _, c := range cases {
		if got := bsdExactOwner(c.out, parseDestNet(c.dest)); got != c.want {
			t.Fatalf("bsdExactOwner(%q)=%q want %q（%s）", c.dest, got, c.want, c.why)
		}
	}
}

// routeLookupDev 与 destOwnerDev 是两个问题，前者的解析器必须能吃下
// `ip route get` 的形状（转发路径查询），这是钉默认网关的依据。
func TestAfterTokenMatchesIPRouteGet(t *testing.T) {
	out := strings.Fields("192.168.100.1 via 192.168.0.1 dev br-lan src 192.168.0.2 uid 0")
	if got := afterToken(out, "dev"); got != "br-lan" {
		t.Fatalf("got %q", got)
	}
	if got := afterToken(out, "via"); got != "192.168.0.1" {
		t.Fatalf("got %q", got)
	}
	if got := afterToken(strings.Fields("10.0.0.1 dev"), "dev"); got != "" {
		t.Fatalf("残缺输出应返回空，got %q", got)
	}
}
