package detector

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// LinuxFirewallDetector Linux 防火墙检测器
type LinuxFirewallDetector struct {
	systemDetector SystemDetector
}

// NewLinuxFirewallDetector 创建 Linux 防火墙检测器
func NewLinuxFirewallDetector(systemDetector SystemDetector) *LinuxFirewallDetector {
	return &LinuxFirewallDetector{
		systemDetector: systemDetector,
	}
}

// DetectFirewall 检测防火墙信息
func (d *LinuxFirewallDetector) DetectFirewall(ctx DetectionContext) (*FirewallInfo, error) {
	info := &FirewallInfo{
		Rules:       []FirewallRule{},
		Zones:       []FirewallZone{},
		PortStates:  []PortState{},
		ChainStates: make(map[string]string),
		Properties:  make(map[string]interface{}),
	}

	// 检测防火墙类型
	firewallType := d.detectFirewallType(ctx)
	info.Type = firewallType

	switch firewallType {
	case "fw3":
		return d.detectOpenWrtFirewall(ctx, info)
	case "iptables":
		return d.detectIptablesFirewall(ctx, info)
	case "ufw":
		return d.detectUfwFirewall(ctx, info)
	case "firewalld":
		return d.detectFirewalldFirewall(ctx, info)
	default:
		info.Type = "unknown"
		info.Enabled = false
		return info, nil
	}
}

// GetCacheKey 获取缓存键
func (d *LinuxFirewallDetector) GetCacheKey() string {
	return "firewall_linux"
}

// IsSupported 检查是否支持当前系统
func (d *LinuxFirewallDetector) IsSupported() bool {
	return d.systemDetector != nil && d.systemDetector.IsSupported()
}

// detectFirewallType 检测防火墙类型
func (d *LinuxFirewallDetector) detectFirewallType(ctx DetectionContext) string {
	// 检测系统类型
	if d.systemDetector != nil {
		if systemInfo, err := d.systemDetector.DetectSystem(ctx); err == nil {
			if systemInfo.OS == "openwrt" {
				return "fw3"
			}
		}
	}

	// 检查 firewalld
	if d.isCommandAvailable("firewall-cmd") {
		if output, err := safeExecCommand("systemctl", "is-active", "firewalld"); err == nil {
			if strings.TrimSpace(string(output)) == "active" {
				return "firewalld"
			}
		}
	}

	// 检查 ufw
	if d.isCommandAvailable("ufw") {
		return "ufw"
	}

	// 检查 iptables
	if d.isCommandAvailable("iptables") {
		return "iptables"
	}

	return "unknown"
}

// detectOpenWrtFirewall 检测 OpenWrt 防火墙
func (d *LinuxFirewallDetector) detectOpenWrtFirewall(ctx DetectionContext, info *FirewallInfo) (*FirewallInfo, error) {
	info.Type = "fw3"

	// 检查 fw3 状态
	info.Enabled = d.isServiceRunning("firewall")

	// 解析 fw3 配置
	if err := d.parseUciFirewallConfig(info); err != nil {
		return info, err
	}

	// 获取 iptables 规则
	if err := d.parseIptablesRules(info); err != nil {
		return info, err
	}

	// 检测端口状态
	d.detectPortStates(info)

	return info, nil
}

// detectIptablesFirewall 检测 iptables 防火墙
func (d *LinuxFirewallDetector) detectIptablesFirewall(ctx DetectionContext, info *FirewallInfo) (*FirewallInfo, error) {
	info.Type = "iptables"
	info.Enabled = d.isCommandAvailable("iptables")

	// 获取 iptables 规则
	if err := d.parseIptablesRules(info); err != nil {
		return info, err
	}

	// 检测端口状态
	d.detectPortStates(info)

	return info, nil
}

// detectUfwFirewall 检测 UFW 防火墙
func (d *LinuxFirewallDetector) detectUfwFirewall(ctx DetectionContext, info *FirewallInfo) (*FirewallInfo, error) {
	info.Type = "ufw"

	// 检查 UFW 状态
	if output, err := safeExecCommand("ufw", "status"); err == nil {
		status := string(output)
		info.Enabled = strings.Contains(status, "Status: active")

		// 解析 UFW 规则
		d.parseUfwRules(info, status)
	}

	// 检测端口状态
	d.detectPortStates(info)

	return info, nil
}

// detectFirewalldFirewall 检测 firewalld 防火墙
func (d *LinuxFirewallDetector) detectFirewalldFirewall(ctx DetectionContext, info *FirewallInfo) (*FirewallInfo, error) {
	info.Type = "firewalld"

	// 检查 firewalld 状态
	if output, err := safeExecCommand("firewall-cmd", "--state"); err == nil {
		info.Enabled = strings.TrimSpace(string(output)) == "running"
	}

	if info.Enabled {
		// 获取区域信息
		d.parseFirewalldZones(info)

		// 获取规则信息
		d.parseFirewalldRules(info)
	}

	// 检测端口状态
	d.detectPortStates(info)

	return info, nil
}

// parseUciFirewallConfig 解析 UCI 防火墙配置
func (d *LinuxFirewallDetector) parseUciFirewallConfig(info *FirewallInfo) error {
	// 解析防火墙区域
	if output, err := safeExecCommand("uci", "show", "firewall"); err == nil {
		lines := strings.Split(string(output), "\n")
		zones := make(map[string]*FirewallZone)

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			// 解析区域配置
			if strings.Contains(line, ".zone[") {
				d.parseUciZoneLine(line, zones)
			}
		}

		// 转换为切片
		for _, zone := range zones {
			info.Zones = append(info.Zones, *zone)
		}
	}

	return nil
}

// parseUciZoneLine 解析 UCI 区域配置行
func (d *LinuxFirewallDetector) parseUciZoneLine(line string, zones map[string]*FirewallZone) {
	// 例如: firewall.@zone[0].name='lan'
	re := regexp.MustCompile(`firewall\.@zone\[(\d+)\]\.(\w+)='?([^']*)'?`)
	matches := re.FindStringSubmatch(line)
	if len(matches) != 4 {
		return
	}

	index := matches[1]
	key := matches[2]
	value := matches[3]

	if zones[index] == nil {
		zones[index] = &FirewallZone{
			Networks:  []string{},
			IsHealthy: true,
		}
	}

	zone := zones[index]
	switch key {
	case "name":
		zone.Name = value
	case "input":
		zone.Input = value
	case "output":
		zone.Output = value
	case "forward":
		zone.Forward = value
	case "masq":
		zone.Masq = value == "1"
	case "network":
		if value != "" {
			zone.Networks = strings.Split(value, " ")
		}
	}
}

// parseIptablesRules 解析 iptables 规则
func (d *LinuxFirewallDetector) parseIptablesRules(info *FirewallInfo) error {
	chains := []string{"INPUT", "OUTPUT", "FORWARD"}

	for _, chain := range chains {
		if output, err := safeExecCommand("iptables", "-L", chain, "-n", "--line-numbers"); err == nil {
			rules := d.parseIptablesOutput(string(output), chain)
			info.Rules = append(info.Rules, rules...)

			// 设置链状态
			if len(rules) > 0 {
				info.ChainStates[chain] = "active"
			} else {
				info.ChainStates[chain] = "empty"
			}
		}
	}

	return nil
}

// parseIptablesOutput 解析 iptables 输出
func (d *LinuxFirewallDetector) parseIptablesOutput(output, chain string) []FirewallRule {
	var rules []FirewallRule
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Chain") || strings.HasPrefix(line, "num") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		rule := FirewallRule{
			Chain:  chain,
			Target: fields[1],
			State:  "active",
		}

		if len(fields) > 2 {
			rule.Protocol = fields[2]
		}
		if len(fields) > 4 {
			rule.Source = fields[4]
		}
		if len(fields) > 5 {
			rule.Dest = fields[5]
		}

		// 尝试提取端口信息
		for i, field := range fields {
			if strings.Contains(field, "dpt:") {
				rule.Port = strings.TrimPrefix(field, "dpt:")
			} else if strings.Contains(field, "spt:") && rule.Port == "" {
				rule.Port = strings.TrimPrefix(field, "spt:")
			} else if i > 6 {
				rule.Comment += field + " "
			}
		}
		rule.Comment = strings.TrimSpace(rule.Comment)

		rules = append(rules, rule)
	}

	return rules
}

// parseUfwRules 解析 UFW 规则
func (d *LinuxFirewallDetector) parseUfwRules(info *FirewallInfo, status string) {
	lines := strings.Split(status, "\n")
	inRulesSection := false

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.Contains(line, "-----") {
			inRulesSection = true
			continue
		}

		if !inRulesSection || line == "" {
			continue
		}

		// 解析规则行
		rule := d.parseUfwRuleLine(line)
		if rule != nil {
			info.Rules = append(info.Rules, *rule)
		}
	}
}

// parseUfwRuleLine 解析 UFW 规则行
func (d *LinuxFirewallDetector) parseUfwRuleLine(line string) *FirewallRule {
	// UFW 格式: [1] 22/tcp ALLOW IN Anywhere
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return nil
	}

	rule := &FirewallRule{
		Chain: "INPUT",
		State: "active",
	}

	// 解析端口/协议
	if strings.Contains(fields[0], "/") {
		parts := strings.Split(fields[0], "/")
		rule.Port = parts[0]
		if len(parts) > 1 {
			rule.Protocol = parts[1]
		}
	}

	// 解析目标动作
	if len(fields) > 1 {
		rule.Target = strings.ToUpper(fields[1])
	}

	// 解析方向
	if len(fields) > 2 {
		direction := fields[2]
		if direction == "OUT" {
			rule.Chain = "OUTPUT"
		}
	}

	// 解析源地址
	if len(fields) > 3 {
		rule.Source = fields[3]
	}

	return rule
}

// parseFirewalldZones 解析 firewalld 区域
func (d *LinuxFirewallDetector) parseFirewalldZones(info *FirewallInfo) {
	if output, err := exec.Command("firewall-cmd", "--get-zones").Output(); err == nil {
		zones := strings.Fields(string(output))
		for _, zoneName := range zones {
			zone := FirewallZone{
				Name:      zoneName,
				Networks:  []string{},
				IsHealthy: true,
			}

			// 获取区域详细信息
			if zoneOutput, err := exec.Command("firewall-cmd", "--zone="+zoneName, "--list-all").Output(); err == nil {
				d.parseFirewalldZoneOutput(string(zoneOutput), &zone)
			}

			info.Zones = append(info.Zones, zone)
		}
	}
}

// parseFirewalldZoneOutput 解析 firewalld 区域输出
func (d *LinuxFirewallDetector) parseFirewalldZoneOutput(output string, zone *FirewallZone) {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "interfaces:") {
			interfaces := strings.TrimSpace(strings.TrimPrefix(line, "interfaces:"))
			if interfaces != "" {
				zone.Networks = strings.Fields(interfaces)
			}
		} else if strings.HasPrefix(line, "masquerade:") {
			masq := strings.TrimSpace(strings.TrimPrefix(line, "masquerade:"))
			zone.Masq = masq == "yes"
		}
	}
}

// parseFirewalldRules 解析 firewalld 规则
func (d *LinuxFirewallDetector) parseFirewalldRules(info *FirewallInfo) {
	// firewalld 的规则比较复杂，这里简化处理
	if output, err := exec.Command("firewall-cmd", "--list-all").Output(); err == nil {
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ports:") {
				ports := strings.TrimSpace(strings.TrimPrefix(line, "ports:"))
				if ports != "" {
					portList := strings.Fields(ports)
					for _, port := range portList {
						rule := FirewallRule{
							Chain:   "INPUT",
							Target:  "ACCEPT",
							Port:    port,
							State:   "active",
							Comment: "firewalld allowed port",
						}
						info.Rules = append(info.Rules, rule)
					}
				}
			}
		}
	}
}

// detectPortStates 检测端口状态
func (d *LinuxFirewallDetector) detectPortStates(info *FirewallInfo) {
	// 使用 ss 命令获取监听端口
	if output, err := exec.Command("ss", "-tuln").Output(); err == nil {
		ports := d.parseSSOutput(string(output), info)
		info.PortStates = ports
	} else if output, err := exec.Command("netstat", "-tuln").Output(); err == nil {
		ports := d.parseNetstatOutput(string(output), info)
		info.PortStates = ports
	}
}

// parseSSOutput 解析 ss 命令输出
func (d *LinuxFirewallDetector) parseSSOutput(output string, info *FirewallInfo) []PortState {
	var ports []PortState
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] == "Netid" {
			continue
		}

		protocol := strings.ToUpper(fields[0])
		if protocol != "TCP" && protocol != "UDP" {
			continue
		}

		state := fields[1]
		localAddr := fields[4]

		// 解析端口
		if colonIndex := strings.LastIndex(localAddr, ":"); colonIndex != -1 {
			portStr := localAddr[colonIndex+1:]
			if port, err := strconv.Atoi(portStr); err == nil {
				portState := PortState{
					Port:     port,
					Protocol: strings.ToLower(protocol),
					State:    state,
					Firewall: d.getPortFirewallState(info, port, protocol),
				}
				ports = append(ports, portState)
			}
		}
	}

	return ports
}

// parseNetstatOutput 解析 netstat 命令输出
func (d *LinuxFirewallDetector) parseNetstatOutput(output string, info *FirewallInfo) []PortState {
	var ports []PortState
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		protocol := strings.ToUpper(fields[0])
		if !strings.HasPrefix(protocol, "TCP") && !strings.HasPrefix(protocol, "UDP") {
			continue
		}

		localAddr := fields[3]
		state := "LISTEN"
		if len(fields) > 5 {
			state = fields[5]
		}

		// 解析端口
		if colonIndex := strings.LastIndex(localAddr, ":"); colonIndex != -1 {
			portStr := localAddr[colonIndex+1:]
			if port, err := strconv.Atoi(portStr); err == nil {
				portState := PortState{
					Port:     port,
					Protocol: strings.ToLower(protocol[:3]),
					State:    state,
					Firewall: d.getPortFirewallState(info, port, protocol[:3]),
				}
				ports = append(ports, portState)
			}
		}
	}

	return ports
}

// getPortFirewallState 获取端口的防火墙状态
func (d *LinuxFirewallDetector) getPortFirewallState(info *FirewallInfo, port int, protocol string) string {
	portStr := strconv.Itoa(port)

	for _, rule := range info.Rules {
		if (rule.Port == portStr || rule.Port == "") &&
			(strings.EqualFold(rule.Protocol, protocol) || rule.Protocol == "") {
			if rule.Target == "ACCEPT" || rule.Target == "ALLOW" {
				return "ALLOWED"
			} else if rule.Target == "DROP" || rule.Target == "REJECT" || rule.Target == "DENY" {
				return "BLOCKED"
			}
		}
	}

	return "UNKNOWN"
}

// isCommandAvailable 检查命令是否可用
func (d *LinuxFirewallDetector) isCommandAvailable(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

// isServiceRunning 检查服务是否运行
func (d *LinuxFirewallDetector) isServiceRunning(service string) bool {
	// 尝试不同的服务管理器
	commands := [][]string{
		{"systemctl", "is-active", service},
		{"service", service, "status"},
		{"/etc/init.d/" + service, "status"},
	}

	for _, cmd := range commands {
		if output, err := exec.Command(cmd[0], cmd[1:]...).Output(); err == nil {
			outputStr := strings.ToLower(string(output))
			if strings.Contains(outputStr, "active") ||
				strings.Contains(outputStr, "running") ||
				strings.Contains(outputStr, "started") {
				return true
			}
		}
	}

	return false
}
