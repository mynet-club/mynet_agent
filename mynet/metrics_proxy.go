package mynet

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// ProxyMetrics 本机 gnb proxy.conf 的实际生效态（供心跳上报与下游诊断）。
// 仅反映磁盘上 gnb 正在热加载的 proxy.conf 事实，不推测云端意图。
type ProxyMetrics struct {
	Configured  bool   `json:"configured"`
	Enabled     bool   `json:"enabled"`
	Role        string `json:"role,omitempty"`
	IngressOpen bool   `json:"ingress_open"`
}

// collectProxyMetrics 读取当前激活节点目录下的 proxy.conf，填进 m.Proxy。
// 文件不存在时视为未配置（Enabled=false, IngressOpen=false）。
func collectProxyMetrics(m *Metrics) {
	dir := gnbConfDir()
	if dir == "" {
		return
	}
	pm := parseProxyConfMetrics(filepath.Join(dir, "proxy.conf"))
	m.Proxy = &pm
}

// parseProxyConfMetrics 按 gnb_proxy.c 的键字口径解析 proxy.conf：
//   - proxy on|1|true → Enabled=true
//   - role client|server|both → Role
//   - ingress_open on|off → IngressOpen
//
// 文件不存在或不可读时返回零值（Configured=false, Enabled=false, IngressOpen=false）。
func parseProxyConfMetrics(path string) ProxyMetrics {
	f, err := os.Open(path)
	if err != nil {
		return ProxyMetrics{}
	}
	defer f.Close()

	pm := ProxyMetrics{Configured: true}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if idx := strings.IndexByte(line, '#'); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "proxy":
			v := strings.ToLower(fields[1])
			pm.Enabled = (v == "on" || v == "1" || v == "true")
		case "role":
			v := strings.ToLower(fields[1])
			if v == "client" || v == "server" || v == "both" {
				pm.Role = v
			}
		case "ingress_open":
			v := strings.ToLower(fields[1])
			pm.IngressOpen = (v == "on" || v == "1" || v == "true")
		}
	}
	return pm
}
