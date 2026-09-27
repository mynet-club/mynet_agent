//go:build windows

package detector

import (
	"encoding/csv"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// WindowsRoutingDetector — F-4 / V5-2。
//
// 接口走 Go 标准库（net.Interfaces 与平台无关、不依赖本地化文案）；
// 路由走 PowerShell `Get-NetRoute` 的 CSV 输出。
//
// 为什么不解析 `route print` / `netstat -rn`：这两条命令的表头与
// 「在链路上」（on-link）都是**按系统 UI 语言本地化**的。真机实测（zh-CN）
// 里网关列叫「网关」、on-link 叫「在链路上」，按英文关键字匹配会整表落空。
// PowerShell 的属性名是稳定的英文标识符，CSV 也不受 UI 语言影响。
type WindowsRoutingDetector struct{}

func (d *WindowsRoutingDetector) DetectRouting(ctx DetectionContext) (*RoutingInfo, error) {
	info := &RoutingInfo{}
	_ = ctx
	winInterfaces(info)
	_ = winRoutes(info) // 路由拿不到就只报接口，不整单失败
	for _, r := range info.Routes {
		if r.Destination == "0.0.0.0/0" {
			info.DefaultGateway = r.Gateway
			info.DefaultIface = r.Interface
			break
		}
	}
	// 默认接口：网关所在接口；找不到就留空，不瞎猜
	if info.DefaultIface != "" {
		for i := range info.Interfaces {
			if info.Interfaces[i].Name == info.DefaultIface {
				info.Interfaces[i].IsDefault = true
				break
			}
		}
	}
	info.Statistics.TotalInterfaces = len(info.Interfaces)
	info.Statistics.TotalRoutes = len(info.Routes)
	for _, i := range info.Interfaces {
		if i.State == "up" {
			info.Statistics.ActiveInterfaces++
		}
	}
	return info, nil
}

func (d *WindowsRoutingDetector) GetCacheKey() string { return "routing:windows" }
func (d *WindowsRoutingDetector) IsSupported() bool   { return runtime.GOOS == "windows" }

// registerPlatformRouting 供 factory_other.go（!linux）调用。
func registerPlatformRouting(f *DetectorFactory) {
	f.RegisterRoutingDetector("windows", func(SystemDetector) RoutingDetector {
		return &WindowsRoutingDetector{}
	})
}

// winInterfaces 用标准库枚举接口。Windows 上 ifi.Name 是
// 「以太网」/「Wi-Fi」这类友好名，与 Get-NetRoute 的 InterfaceAlias 同源。
func winInterfaces(info *RoutingInfo) {
	ifs, err := net.Interfaces()
	if err != nil {
		return
	}
	for _, ifi := range ifs {
		ni := NetworkInterface{
			Name: ifi.Name,
			MTU:  ifi.MTU,
			MAC:  ifi.HardwareAddr.String(),
			Type: winIfaceType(ifi),
		}
		if ifi.Flags&net.FlagUp != 0 {
			ni.State = "up"
		} else {
			ni.State = "down"
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue // 只收 IPv4，与路由表口径一致
			}
			ni.IPs = append(ni.IPs, ipn.IP.String())
		}
		info.Interfaces = append(info.Interfaces, ni)
	}
}

func winIfaceType(ifi net.Interface) string {
	if ifi.Flags&net.FlagLoopback != 0 {
		return "loopback"
	}
	name := strings.ToLower(ifi.Name)
	switch {
	case strings.Contains(name, "wi-fi") || strings.Contains(name, "wireless") || strings.Contains(name, "wlan"):
		return "wifi"
	case strings.Contains(name, "vpn") || strings.Contains(name, "tap") || strings.Contains(name, "wintun") || strings.Contains(name, "gnb"):
		return "vpn"
	default:
		return "ethernet"
	}
}

// winRoutes 用 Get-NetRoute 取 IPv4 路由表。字段名是稳定的英文标识符，
// 不受系统显示语言影响。
//
// CSV 而不是 JSON：PS 5.1 的 ConvertTo-Json 对单对象不包数组括号，
// 解析要分情况；CSV 永远是一行一条。
func winRoutes(info *RoutingInfo) error {
	// ifIndex → 接口名，路由表只给索引
	idxName := map[int]string{}
	ifs, err := net.Interfaces()
	if err == nil {
		for _, ifi := range ifs {
			idxName[ifi.Index] = ifi.Name
		}
	}

	ps := `Get-NetRoute -AddressFamily IPv4 | Select-Object DestinationPrefix,NextHop,ifIndex,RouteMetric | ConvertTo-Csv -NoTypeInformation`
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if err != nil {
		return err
	}
	r := csv.NewReader(strings.NewReader(string(out)))
	rows, err := r.ReadAll()
	if err != nil || len(rows) < 2 {
		return err
	}
	// rows[0] = 表头
	for _, row := range rows[1:] {
		if len(row) < 4 {
			continue
		}
		ifIndex, _ := strconv.Atoi(strings.TrimSpace(row[2]))
		metric, _ := strconv.Atoi(strings.TrimSpace(row[3]))
		gw := strings.TrimSpace(row[1])
		if gw == "0.0.0.0" {
			gw = "on-link"
		}
		info.Routes = append(info.Routes, RouteEntry{
			Destination: strings.TrimSpace(row[0]),
			Gateway:     gw,
			Interface:   idxName[ifIndex],
			Metric:      metric,
			IsActive:    true,
		})
	}
	return nil
}
