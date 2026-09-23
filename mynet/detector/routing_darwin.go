//go:build darwin

package detector

import (
	"os/exec"
	"runtime"
	"strings"
)

// DarwinRoutingDetector — 从旧 tools/routing 的 Darwin 分支收敛而来（F-1）。
// 类型对齐 detector.RoutingInfo，不再维护第二套 RouteInfo。
type DarwinRoutingDetector struct{}

func (d *DarwinRoutingDetector) DetectRouting(ctx DetectionContext) (*RoutingInfo, error) {
	info := &RoutingInfo{}
	_ = ctx
	if err := darwinDefaultGateway(info); err != nil {
		return info, err
	}
	if err := darwinRouteTable(info); err != nil {
		return info, err
	}
	if err := darwinInterfaces(info); err != nil {
		return info, err
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

func (d *DarwinRoutingDetector) GetCacheKey() string { return "routing:darwin" }
func (d *DarwinRoutingDetector) IsSupported() bool   { return runtime.GOOS == "darwin" }

func darwinDefaultGateway(info *RoutingInfo) error {
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			switch strings.TrimSuffix(fields[0], ":") {
			case "gateway":
				info.DefaultGateway = fields[1]
			case "interface":
				info.DefaultIface = fields[1]
			}
		}
	}
	return nil
}

func darwinRouteTable(info *RoutingInfo) error {
	out, err := exec.Command("netstat", "-rn", "-f", "inet").Output()
	if err != nil {
		return err
	}
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if i < 4 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		info.Routes = append(info.Routes, RouteEntry{
			Destination: fields[0],
			Gateway:     fields[1],
			Interface:   fields[len(fields)-1],
			IsActive:    true,
		})
	}
	return nil
}

func darwinInterfaces(info *RoutingInfo) error {
	out, err := exec.Command("ifconfig", "-a").Output()
	if err != nil {
		return err
	}
	var cur *NetworkInterface
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") && strings.Contains(line, ":") {
			name := strings.SplitN(line, ":", 2)[0]
			cur = &NetworkInterface{Name: name, State: "down", Type: "ethernet"}
			if strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "wg") {
				cur.Type = "vpn"
			}
			info.Interfaces = append(info.Interfaces, *cur)
			cur = &info.Interfaces[len(info.Interfaces)-1]
			continue
		}
		if cur == nil {
			continue
		}
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "inet ") {
			f := strings.Fields(trim)
			if len(f) >= 2 {
				cur.IPs = append(cur.IPs, f[1])
				cur.State = "up"
			}
		}
		if strings.Contains(trim, "ether ") {
			f := strings.Fields(trim)
			if len(f) >= 2 {
				cur.MAC = f[1]
			}
		}
		if strings.Contains(trim, "mtu ") {
			var mtu int
			if n, _ := sscanfMTU(trim, &mtu); n == 1 {
				cur.MTU = mtu
			}
		}
	}
	return nil
}

func sscanfMTU(s string, mtu *int) (int, error) {
	// "mtu 1500" 形态
	f := strings.Fields(s)
	for i := 0; i < len(f)-1; i++ {
		if f[i] == "mtu" {
			n := 0
			for _, c := range f[i+1] {
				if c < '0' || c > '9' {
					return 0, nil
				}
				n = n*10 + int(c-'0')
			}
			*mtu = n
			return 1, nil
		}
	}
	return 0, nil
}
