package main

import (
	"net"
	"strings"
	"sync"
)

// egressIPCache 非阻塞保存节点观测到的出网 IP（由心跳/deviceinfo 响应回填或异步探测更新）。
//
// 设计原则：
//   - 心跳路径零阻塞：beat() 只做 RLock 读字符串，绝不做同步外网探测；
//   - 出网 IP 变化时自动带入下一拍心跳的 node_monitor.node_self.wan_ipv4/wanip，
//     由后端记录变更日志并更新 node_device_infos.public_ip。
type egressIPCache struct {
	mu sync.RWMutex
	ip string
}

func (c *egressIPCache) Get() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ip
}

// Update 更新缓存的出网 IP，返回 (是否发生变化, 旧值)。
func (c *egressIPCache) Update(newIP string) (bool, string) {
	if c == nil {
		return false, ""
	}
	clean := strings.TrimSpace(newIP)
	if clean == "" || net.ParseIP(clean) == nil {
		return false, ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ip == clean {
		return false, c.ip
	}
	old := c.ip
	c.ip = clean
	return true, old
}

// collectLocalNonLoopbackIPs 采集本机真实网卡的非回环 IPv4（跳过 TUN/utun 虚拟口），
// 仅在开机上报 deviceinfo 时调用一次，不引入外部命令或重型依赖。
func collectLocalNonLoopbackIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(iface.Name)
		if strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "tun") ||
			strings.HasPrefix(name, "gnb") || strings.HasPrefix(name, "wg") ||
			strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}
			v4 := ip.To4()
			if v4 == nil {
				continue
			}
			s := v4.String()
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}
