//go:build !windows

package localapi

import (
	"net"
	"os/exec"
	"strings"
)

// osRouteExists 查系统路由表里有没有这个目的地。
// linux 用 `ip route show`，darwin 用 `route -n get`。
//
// ⚠️ 必须用 lookPathAny 解析出绝对路径再 exec：daemon 的 PATH 往往没有
// /sbin，而 route/ifconfig 就在那儿。症状是「终端里查得到、node-status 说缺失」
// —— 与 apply 曾经的 `exec: "route": not found` 同一坑。
func osRouteExists(dest string) bool {
	// 裸 IP 当 host 查；带前缀的当整条查
	if _, _, err := net.ParseCIDR(dest); err == nil {
		host := dest
		if ip, _, err := net.ParseCIDR(dest); err == nil {
			host = ip.String()
		}
		// linux: ip route show <cidr> 有输出即存在
		if ipBin := lookPathAny("ip"); ipBin != "" {
			out, err := exec.Command(ipBin, "route", "show", dest).Output()
			if err == nil {
				return len(strings.TrimSpace(string(out))) > 0
			}
		}
		return darwinRouteGet(host)
	}
	return darwinRouteGet(dest)
}

func darwinRouteGet(host string) bool {
	bin := lookPathAny("route")
	if bin == "" {
		return false
	}
	out, err := exec.Command(bin, "-n", "get", host).Output()
	if err != nil {
		return false
	}
	s := string(out)
	// route get 对「走默认网关」的目标也 exit 0，要看 destination 是不是 host 本身
	return strings.Contains(s, host) && !strings.Contains(s, "destination: default")
}

// localRouteExists 判断目标是否已经由物理网卡承载。物理网络本来就不应
// 再安装到 MyNet 隧道，否则会覆盖用户当前局域网。
func localRouteExists(host string) bool {
	bin := lookPathAny("route")
	if bin == "" {
		return false
	}
	// route get 对网段地址本身（例如 192.168.0.0）常返回 default；
	// 用该网段的第一个主机地址查询，才能识别物理网卡上的直连网段。
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil && ip.To4()[3] == 0 {
		v := ip.To4()
		v[3] = 1
		host = v.String()
	}
	out, err := exec.Command(bin, "-n", "get", host).Output()
	if err != nil {
		return false
	}
	s := string(out)
	if strings.Contains(s, "destination: default") {
		return false
	}
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "interface:" {
			return !strings.HasPrefix(f[1], "utun") && !strings.HasPrefix(f[1], "tun") && !strings.HasPrefix(f[1], "gnb") && !strings.HasPrefix(f[1], "wg")
		}
	}
	return false
}
