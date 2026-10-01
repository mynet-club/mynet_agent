//go:build !windows

package mynet

import (
	"bytes"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"strings"
)

// procRouteFile 内核 IPv4 主路由表，Linux（含 busybox/OpenWrt）由 procfs 提供。
const procRouteFile = "/proc/net/route"

// destOwnerDev 目的地**这条路由本身**挂在哪块网卡上；无主返回 ""。
//
// 判定必须是「前缀完全相等的 FIB 条目」，不能是路由查询的结果。
// `ip route get` / `route -n get` 做的是最长前缀匹配、默认路由兜底，
// 隧道重启把路由清光后，任意目的地都会由默认路由接住并回一个物理口 ——
// 那是**谁替我转发**，不是**谁拥有这条路由**。当成所有权，apply 的第二道闸
// （只删自己装的）就把我们本该装的路由永久跳过了。
// .182（公司网关）现场实测：FIB 里没有 192.168.100.0/24，
// 而 `ip route get 192.168.100.1` → `via 192.168.0.1 dev br-lan` →
// owner=br-lan ≠ gnb_tun_16 → 整条 skip，路由再也装不回去。
//
// Linux 读 /proc/net/route（精确、免子进程，busybox 的 route(8) 不支持 -n get）；
// BSD 用 `route -n get`，但要求输出的 destination/mask 与本尊相等才算。
func destOwnerDev(dest string) string {
	if dest == "" {
		return ""
	}
	want := parseDestNet(dest)
	if want != nil {
		if b, err := os.ReadFile(procRouteFile); err == nil {
			return procRouteOwner(string(b), want)
		}
		out := bsdRouteGet(dest)
		return bsdExactOwner(out, want)
	}
	// IPv6：仍按查询结果判（apply 只装 IPv4 站点路由，此处保守沿用旧语义）
	out := bsdRouteGet(dest)
	if strings.Contains(out, "destination: default") {
		return ""
	}
	return bsdRouteInterface(out)
}

// routeLookupDev 目的地此刻实际从哪块网卡出去（最长前缀匹配 + 默认路由兜底）。
//
// 与 destOwnerDev 是两个问题，别混用：这里问**转发路径**，用于判断默认网关
// 有没有被隧道的覆盖路由盖住（gatewayHijacked）。网关通常没有自己的 /32，
// 只能靠覆盖路由看出来，所以这里要的正是「谁替我转发」。
func routeLookupDev(dest string) string {
	if dest == "" {
		return ""
	}
	if ipBin, err := exec.LookPath("ip"); err == nil {
		out, _ := exec.Command(ipBin, "route", "get", hostDestIP(dest)).Output()
		if dev := afterToken(strings.Fields(string(out)), "dev"); dev != "" {
			return dev
		}
	}
	return bsdRouteInterface(bsdRouteGet(dest))
}

func afterToken(fields []string, token string) string {
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == token {
			return fields[i+1]
		}
	}
	return ""
}

// bsdRouteGet `route -n get`：主机路由不带 /32，网段要 -net。
func bsdRouteGet(dest string) string {
	if isHostDest(dest) {
		out, _ := exec.Command("route", "-n", "get", hostDestIP(dest)).Output()
		return string(out)
	}
	_, ipnet, err := net.ParseCIDR(dest)
	if err != nil {
		return ""
	}
	out, _ := exec.Command("route", "-n", "get", "-net", ipnet.IP.String()).Output()
	return string(out)
}

// bsdRouteInterface 取 route get 输出里的 interface: 行。
func bsdRouteInterface(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	return ""
}

// bsdExactOwner 把 route get 的输出当 FIB 条目来核：destination(+mask) 必须
// 与 want 完全相等，否则视为无主。默认路由、别人的覆盖路由、直连口替答
// 都在这里被挡掉（busybox 无 route get 时输出为空 → 无主）。
func bsdExactOwner(out string, want *net.IPNet) string {
	if out == "" || want == nil {
		return ""
	}
	var dest, mask, iface string
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "destination":
			dest = value
		case "mask":
			mask = value
		case "interface":
			iface = value
		}
	}
	if iface == "" {
		return ""
	}
	if !samePrefix(dest, mask, want) {
		return ""
	}
	return iface
}

// samePrefix dest/mask 两行拼出的前缀是否就是 want。
// macOS 对主机路由只打 destination 不打 mask（隐含 /32）；mask 也可能是
// "default"，那不是 want（apply 从不按默认路由主张所有权）。
func samePrefix(dest, mask string, want *net.IPNet) bool {
	if dest == "" || dest == "default" {
		return false
	}
	if strings.Contains(dest, "/") {
		got := parseDestNet(dest)
		return got != nil && got.String() == want.String()
	}
	ip := net.ParseIP(dest)
	if ip == nil || ip.To4() == nil {
		return false
	}
	m := net.CIDRMask(32, 32)
	if mask != "" && mask != "default" {
		if pm := net.ParseIP(mask); pm != nil && pm.To4() != nil {
			m = net.IPMask(pm.To4())
		} else {
			return false
		}
	}
	if !bytes.Equal(m, want.Mask) {
		return false
	}
	return net.IP(ip.To4()).Mask(m).Equal(want.IP)
}

// procRouteOwner 在 /proc/net/route 内容里精确查 want，返回接口名；无则 ""。
//
// 行格式：Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT
// Destination/Mask 是**小端 hex**：10.182.236.184 → B8ECB60A。
// 默认路由的掩码是 00000000，与任何 want 都不相等，天然不会被算成 owner。
func procRouteOwner(text string, want *net.IPNet) string {
	if want == nil {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[0] == "Iface" {
			continue
		}
		dest, mask := unhexLE(f[1]), unhexLE(f[7])
		if dest == nil || mask == nil || !bytes.Equal(mask, want.Mask) {
			continue
		}
		if net.IP(dest).Mask(net.IPMask(mask)).Equal(want.IP) {
			return f[0]
		}
	}
	return ""
}

// unhexLE 小端 hex（内核按 host u32 打印）→ 4 字节 IPv4；非法返回 nil。
func unhexLE(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 4 {
		return nil
	}
	return []byte{b[3], b[2], b[1], b[0]}
}

// routesOnDevice 列出 dev 上的 IPv4 目的地（prefix 形式）。
func routesOnDevice(dev string) []string {
	if ipBin, err := exec.LookPath("ip"); err == nil {
		out, _ := exec.Command(ipBin, "route", "show", "dev", dev).Output()
		var dests []string
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if fields[0] == "local" || fields[0] == "broadcast" || fields[0] == "multicast" {
				continue
			}
			if !strings.Contains(fields[0], "/") && net.ParseIP(fields[0]) == nil {
				continue
			}
			dests = append(dests, fields[0])
		}
		return dests
	}
	// darwin netstat
	out, _ := exec.Command("netstat", "-rn", "-f", "inet").Output()
	var dests []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		for i, f := range fields {
			if f == dev && i >= 1 {
				dest := fields[0]
				if dest == "Destination" || dest == "default" || strings.HasPrefix(dest, "Internet") {
					break
				}
				dests = append(dests, normalizeBSDDest(dest))
				break
			}
		}
	}
	return dests
}

// defaultGateway 读系统默认网关。
func defaultGateway() (gw, dev string) {
	if ipBin, err := exec.LookPath("ip"); err == nil {
		out, err := exec.Command(ipBin, "route", "show", "default").Output()
		if err != nil {
			return "", ""
		}
		fields := strings.Fields(string(out))
		for i := 0; i < len(fields); i++ {
			if fields[i] == "via" && i+1 < len(fields) {
				gw = fields[i+1]
			}
			if fields[i] == "dev" && i+1 < len(fields) {
				dev = fields[i+1]
			}
		}
		return gw, dev
	}
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			gw = strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
		}
		if strings.HasPrefix(line, "interface:") {
			dev = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	return gw, dev
}
