package detector

import (
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// LinuxRoutingDetector Linux 路由检测器
type LinuxRoutingDetector struct {
	systemDetector SystemDetector
}

// NewLinuxRoutingDetector 创建 Linux 路由检测器
func NewLinuxRoutingDetector(systemDetector SystemDetector) *LinuxRoutingDetector {
	return &LinuxRoutingDetector{
		systemDetector: systemDetector,
	}
}

// DetectRouting 检测路由信息
func (d *LinuxRoutingDetector) DetectRouting(ctx DetectionContext) (*RoutingInfo, error) {
	info := &RoutingInfo{
		Interfaces: []NetworkInterface{},
		Routes:     []RouteEntry{},
		Statistics: RouteStatistics{},
	}

	// 检测网络接口
	if err := d.detectInterfaces(info); err != nil {
		return info, fmt.Errorf("failed to detect interfaces: %w", err)
	}

	// 检测路由表
	if err := d.detectRoutes(info); err != nil {
		return info, fmt.Errorf("failed to detect routes: %w", err)
	}

	// 检测默认路由
	d.detectDefaultRoute(info)

	// 计算统计信息
	d.calculateStatistics(info)

	return info, nil
}

// GetCacheKey 获取缓存键
func (d *LinuxRoutingDetector) GetCacheKey() string {
	return "routing_linux"
}

// IsSupported 检查是否支持当前系统
func (d *LinuxRoutingDetector) IsSupported() bool {
	return d.systemDetector != nil && d.systemDetector.IsSupported()
}

// detectInterfaces 检测网络接口
func (d *LinuxRoutingDetector) detectInterfaces(info *RoutingInfo) error {
	// 使用 ip 命令获取接口信息
	if output, err := exec.Command("ip", "addr", "show").Output(); err == nil {
		interfaces := d.parseIPAddrOutput(string(output))
		info.Interfaces = interfaces
		return nil
	}

	// 回退到 ifconfig
	if output, err := exec.Command("ifconfig").Output(); err == nil {
		interfaces := d.parseIfconfigOutput(string(output))
		info.Interfaces = interfaces
		return nil
	}

	return fmt.Errorf("no available network interface command")
}

// parseIPAddrOutput 解析 ip addr 输出
func (d *LinuxRoutingDetector) parseIPAddrOutput(output string) []NetworkInterface {
	var interfaces []NetworkInterface
	var currentInterface *NetworkInterface

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// 解析接口行: 1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 ...
		if matched, _ := regexp.MatchString(`^\d+:\s+\w+:`, line); matched {
			if currentInterface != nil {
				interfaces = append(interfaces, *currentInterface)
			}

			currentInterface = &NetworkInterface{
				IPs: []string{},
			}

			// 提取接口名
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				name := strings.TrimSuffix(parts[1], ":")
				currentInterface.Name = name
				currentInterface.Type = d.guessInterfaceType(name)
			}

			// 提取状态和 MTU
			if strings.Contains(line, "UP") {
				currentInterface.State = "up"
			} else {
				currentInterface.State = "down"
			}

			// 提取 MTU
			if mtuMatch := regexp.MustCompile(`mtu (\d+)`).FindStringSubmatch(line); len(mtuMatch) > 1 {
				if mtu, err := strconv.Atoi(mtuMatch[1]); err == nil {
					currentInterface.MTU = mtu
				}
			}

		} else if currentInterface != nil {
			// 解析属性行
			if strings.HasPrefix(line, "link/") {
				// link/ether 00:15:5d:01:02:03 brd ff:ff:ff:ff:ff:ff
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					currentInterface.MAC = parts[1]
				}
			} else if strings.HasPrefix(line, "inet ") {
				// inet 192.168.1.100/24 brd 192.168.1.255 scope global eth0
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					ip := parts[1]
					currentInterface.IPs = append(currentInterface.IPs, ip)
				}
			}
		}
	}

	// 添加最后一个接口
	if currentInterface != nil {
		interfaces = append(interfaces, *currentInterface)
	}

	return interfaces
}

// parseIfconfigOutput 解析 ifconfig 输出
func (d *LinuxRoutingDetector) parseIfconfigOutput(output string) []NetworkInterface {
	var interfaces []NetworkInterface
	var currentInterface *NetworkInterface

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		// 新接口开始
		if len(line) > 0 && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			if currentInterface != nil {
				interfaces = append(interfaces, *currentInterface)
			}

			currentInterface = &NetworkInterface{
				IPs: []string{},
			}

			// 提取接口名
			parts := strings.Fields(line)
			if len(parts) > 0 {
				name := parts[0]
				if strings.Contains(name, ":") {
					name = strings.Split(name, ":")[0]
				}
				currentInterface.Name = name
				currentInterface.Type = d.guessInterfaceType(name)
			}

			// 检查状态
			if strings.Contains(line, "UP") {
				currentInterface.State = "up"
			} else {
				currentInterface.State = "down"
			}

		} else if currentInterface != nil {
			line = strings.TrimSpace(line)

			// 解析 IP 地址
			if strings.HasPrefix(line, "inet ") {
				parts := strings.Fields(line)
				for i, part := range parts {
					if part == "inet" && i+1 < len(parts) {
						ip := parts[i+1]
						// 移除 addr: 前缀（如果存在）
						if strings.HasPrefix(ip, "addr:") {
							ip = strings.TrimPrefix(ip, "addr:")
						}
						currentInterface.IPs = append(currentInterface.IPs, ip)
						break
					}
				}
			}

			// 解析 MAC 地址
			if strings.Contains(line, "ether ") || strings.Contains(line, "HWaddr ") {
				parts := strings.Fields(line)
				for i, part := range parts {
					if (part == "ether" || part == "HWaddr") && i+1 < len(parts) {
						currentInterface.MAC = parts[i+1]
						break
					}
				}
			}

			// 解析 MTU
			if strings.Contains(line, "MTU:") {
				if mtuMatch := regexp.MustCompile(`MTU:(\d+)`).FindStringSubmatch(line); len(mtuMatch) > 1 {
					if mtu, err := strconv.Atoi(mtuMatch[1]); err == nil {
						currentInterface.MTU = mtu
					}
				}
			}
		}
	}

	// 添加最后一个接口
	if currentInterface != nil {
		interfaces = append(interfaces, *currentInterface)
	}

	return interfaces
}

// guessInterfaceType 推测接口类型
func (d *LinuxRoutingDetector) guessInterfaceType(name string) string {
	name = strings.ToLower(name)

	switch {
	case name == "lo":
		return "loopback"
	case strings.HasPrefix(name, "eth"):
		return "ethernet"
	case strings.HasPrefix(name, "wlan") || strings.HasPrefix(name, "wifi") || strings.HasPrefix(name, "wlp"):
		return "wifi"
	case strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "tap"):
		return "vpn"
	case strings.HasPrefix(name, "br"):
		return "bridge"
	case strings.HasPrefix(name, "docker"):
		return "container"
	case strings.HasPrefix(name, "veth"):
		return "virtual"
	case strings.HasPrefix(name, "ppp"):
		return "ppp"
	case strings.HasPrefix(name, "wg"):
		return "wireguard"
	default:
		return "other"
	}
}

// detectRoutes 检测路由表
func (d *LinuxRoutingDetector) detectRoutes(info *RoutingInfo) error {
	// 使用 ip route 命令
	if output, err := exec.Command("ip", "route", "show").Output(); err == nil {
		routes := d.parseIPRouteOutput(string(output))
		info.Routes = routes
		return nil
	}

	// 回退到 route 命令
	if output, err := exec.Command("route", "-n").Output(); err == nil {
		routes := d.parseRouteOutput(string(output))
		info.Routes = routes
		return nil
	}

	return fmt.Errorf("no available routing command")
}

// parseIPRouteOutput 解析 ip route 输出
func (d *LinuxRoutingDetector) parseIPRouteOutput(output string) []RouteEntry {
	var routes []RouteEntry

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		route := d.parseIPRouteLine(line)
		if route != nil {
			routes = append(routes, *route)
		}
	}

	return routes
}

// parseIPRouteLine 解析 ip route 行
func (d *LinuxRoutingDetector) parseIPRouteLine(line string) *RouteEntry {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return nil
	}

	route := &RouteEntry{
		IsActive: true,
		Type:     "unicast",
	}

	// 第一个字段是目标网络
	destination := parts[0]
	if destination == "default" {
		route.Destination = "0.0.0.0/0"
		route.Type = "default"
	} else {
		route.Destination = destination
	}

	// 解析其余字段
	for i := 1; i < len(parts); i++ {
		switch parts[i] {
		case "via":
			if i+1 < len(parts) {
				route.Gateway = parts[i+1]
				i++
			}
		case "dev":
			if i+1 < len(parts) {
				route.Interface = parts[i+1]
				i++
			}
		case "proto":
			if i+1 < len(parts) {
				route.Protocol = parts[i+1]
				i++
			}
		case "scope":
			if i+1 < len(parts) {
				route.Scope = parts[i+1]
				i++
			}
		case "metric":
			if i+1 < len(parts) {
				if metric, err := strconv.Atoi(parts[i+1]); err == nil {
					route.Metric = metric
				}
				i++
			}
		case "src":
			// 跳过源地址
			if i+1 < len(parts) {
				i++
			}
		}
	}

	return route
}

// parseRouteOutput 解析 route -n 输出
func (d *LinuxRoutingDetector) parseRouteOutput(output string) []RouteEntry {
	var routes []RouteEntry

	lines := strings.Split(output, "\n")
	headerFound := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 跳过头部
		if strings.Contains(line, "Destination") || strings.Contains(line, "Kernel") {
			headerFound = true
			continue
		}

		if !headerFound {
			continue
		}

		route := d.parseRouteLine(line)
		if route != nil {
			routes = append(routes, *route)
		}
	}

	return routes
}

// parseRouteLine 解析 route 行
func (d *LinuxRoutingDetector) parseRouteLine(line string) *RouteEntry {
	fields := strings.Fields(line)
	if len(fields) < 8 {
		return nil
	}

	route := &RouteEntry{
		Destination: fields[0],
		Gateway:     fields[1],
		Interface:   fields[7],
		IsActive:    true,
		Protocol:    "kernel",
	}

	// 处理默认路由
	if route.Destination == "0.0.0.0" {
		route.Destination = "0.0.0.0/0"
		route.Type = "default"
	}

	// 处理网关
	if route.Gateway == "0.0.0.0" {
		route.Gateway = ""
	}

	// 解析标志
	if len(fields) >= 4 {
		flags := fields[3]
		route.Flags = flags

		// 根据标志设置类型
		if strings.Contains(flags, "U") {
			route.IsActive = true
		}
	}

	// 解析 metric
	if len(fields) >= 6 {
		if metric, err := strconv.Atoi(fields[5]); err == nil {
			route.Metric = metric
		}
	}

	return route
}

// detectDefaultRoute 检测默认路由
func (d *LinuxRoutingDetector) detectDefaultRoute(info *RoutingInfo) {
	for _, route := range info.Routes {
		if route.Destination == "0.0.0.0/0" || route.Type == "default" {
			info.DefaultGateway = route.Gateway
			info.DefaultIface = route.Interface

			// 标记默认接口
			for i := range info.Interfaces {
				if info.Interfaces[i].Name == route.Interface {
					info.Interfaces[i].IsDefault = true
					break
				}
			}
			break
		}
	}
}

// calculateStatistics 计算统计信息
func (d *LinuxRoutingDetector) calculateStatistics(info *RoutingInfo) {
	stats := &info.Statistics

	stats.TotalInterfaces = len(info.Interfaces)
	stats.TotalRoutes = len(info.Routes)

	// 统计活跃接口
	for _, iface := range info.Interfaces {
		if iface.State == "up" {
			stats.ActiveInterfaces++
		}
	}

	// 统计节点路由（非默认、非本地的路由）
	for _, route := range info.Routes {
		if route.Type != "default" &&
			!strings.HasPrefix(route.Destination, "127.") &&
			!strings.HasPrefix(route.Destination, "169.254.") &&
			route.Destination != "::1/128" &&
			route.IsActive {

			// 检查是否是点对点或隧道路由
			if d.isNodeRoute(route) {
				stats.NodeRoutes++
			}
		}
	}
}

// isNodeRoute 判断是否是节点路由
func (d *LinuxRoutingDetector) isNodeRoute(route RouteEntry) bool {
	// 检查接口类型
	interfaceType := d.guessInterfaceType(route.Interface)
	if interfaceType == "vpn" || interfaceType == "wireguard" {
		return true
	}

	// 检查目标网络
	if ip, ipnet, err := net.ParseCIDR(route.Destination); err == nil {
		// 检查是否是点对点路由
		ones, bits := ipnet.Mask.Size()
		if bits == 32 && ones == 32 { // IPv4 主机路由
			return true
		}
		if bits == 128 && ones == 128 { // IPv6 主机路由
			return true
		}

		// 检查是否是小网段（可能是 VPN 网络）
		if bits == 32 && ones >= 24 { // /24 或更小的网络
			// 排除常见的局域网网段
			if !d.isCommonLANNetwork(ip) {
				return true
			}
		}
	}

	// 检查协议
	if route.Protocol == "bird" || route.Protocol == "zebra" || route.Protocol == "static" {
		return true
	}

	return false
}

// isCommonLANNetwork 检查是否是常见的局域网网络
func (d *LinuxRoutingDetector) isCommonLANNetwork(ip net.IP) bool {
	// 192.168.0.0/16
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
		// 172.16.0.0/12
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		// 10.0.0.0/8
		if ip4[0] == 10 {
			return true
		}
	}

	return false
}
