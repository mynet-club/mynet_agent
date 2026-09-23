package detector

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LinuxServiceDetector Linux 服务检测器
type LinuxServiceDetector struct {
	systemDetector SystemDetector
}

// NewLinuxServiceDetector 创建 Linux 服务检测器
func NewLinuxServiceDetector(systemDetector SystemDetector) *LinuxServiceDetector {
	return &LinuxServiceDetector{
		systemDetector: systemDetector,
	}
}

// DetectService 检测服务信息
func (d *LinuxServiceDetector) DetectService(ctx DetectionContext, serviceName string) (*ServiceInfo, error) {
	info := &ServiceInfo{
		Name:       serviceName,
		Properties: make(map[string]string),
	}

	// 检测服务管理器类型
	serviceType := d.detectServiceManagerType(ctx)
	info.Type = serviceType

	switch serviceType {
	case "systemd":
		return d.detectSystemdService(ctx, serviceName, info)
	case "procd":
		return d.detectProcdService(ctx, serviceName, info)
	case "sysvinit":
		return d.detectSysVInitService(ctx, serviceName, info)
	case "openrc":
		return d.detectOpenRCService(ctx, serviceName, info)
	default:
		return d.detectGenericService(ctx, serviceName, info)
	}
}

// GetCacheKey 获取缓存键
func (d *LinuxServiceDetector) GetCacheKey(serviceName string) string {
	return fmt.Sprintf("service_linux_%s", serviceName)
}

// IsSupported 检查是否支持当前系统
func (d *LinuxServiceDetector) IsSupported() bool {
	return d.systemDetector != nil && d.systemDetector.IsSupported()
}

// detectServiceManagerType 检测服务管理器类型
func (d *LinuxServiceDetector) detectServiceManagerType(ctx DetectionContext) string {
	// 检测系统类型
	if d.systemDetector != nil {
		if systemInfo, err := d.systemDetector.DetectSystem(ctx); err == nil {
			if systemInfo.OS == "openwrt" {
				return "procd"
			}
		}
	}

	// 检查 systemd
	if d.isCommandAvailable("systemctl") {
		if output, err := exec.Command("systemctl", "--version").Output(); err == nil {
			if strings.Contains(string(output), "systemd") {
				return "systemd"
			}
		}
	}

	// 检查 OpenRC
	if d.isCommandAvailable("rc-service") || d.isCommandAvailable("rc-status") {
		return "openrc"
	}

	// 检查 SysV Init
	if _, err := os.Stat("/etc/init.d"); err == nil {
		return "sysvinit"
	}

	return "unknown"
}

// detectSystemdService 检测 systemd 服务
func (d *LinuxServiceDetector) detectSystemdService(ctx DetectionContext, serviceName string, info *ServiceInfo) (*ServiceInfo, error) {
	info.Type = "systemd"

	// 检查服务是否存在
	if output, err := exec.Command("systemctl", "cat", serviceName).Output(); err == nil {
		info.IsInstalled = true
		d.parseSystemdServiceFile(string(output), info)
	} else {
		info.IsInstalled = false
		info.ErrorMsg = "Service unit not found"
		return info, nil
	}

	// 检查启用状态
	if output, err := exec.Command("systemctl", "is-enabled", serviceName).Output(); err == nil {
		status := strings.TrimSpace(string(output))
		info.IsEnabled = (status == "enabled")
		info.Properties["enable_status"] = status
	}

	// 检查运行状态
	if output, err := exec.Command("systemctl", "is-active", serviceName).Output(); err == nil {
		status := strings.TrimSpace(string(output))
		info.IsRunning = (status == "active")
		info.Properties["active_status"] = status
	}

	// 获取详细状态信息
	if output, err := exec.Command("systemctl", "status", serviceName, "--no-pager", "-l").Output(); err == nil {
		d.parseSystemdStatus(string(output), info)
	}

	// 检查服务文件路径
	if output, err := exec.Command("systemctl", "show", serviceName, "-p", "FragmentPath").Output(); err == nil {
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "FragmentPath=") {
				path := strings.TrimPrefix(line, "FragmentPath=")
				if path != "" && path != "[not set]" {
					info.ServicePath = path
				}
			}
		}
	}

	// 检查配置一致性
	d.checkSystemdConfigConsistency(serviceName, info)

	return info, nil
}

// detectProcdService 检测 procd 服务（OpenWrt）
func (d *LinuxServiceDetector) detectProcdService(ctx DetectionContext, serviceName string, info *ServiceInfo) (*ServiceInfo, error) {
	info.Type = "procd"

	// 检查服务脚本是否存在
	initScriptPath := fmt.Sprintf("/etc/init.d/%s", serviceName)
	if _, err := os.Stat(initScriptPath); err == nil {
		info.IsInstalled = true
		info.ServicePath = initScriptPath

		// 解析初始化脚本
		if content, err := os.ReadFile(initScriptPath); err == nil {
			d.parseProcdInitScript(string(content), info)
		}
	} else {
		info.IsInstalled = false
		info.ErrorMsg = "Init script not found"
		return info, nil
	}

	// 检查启用状态
	if _, err := exec.Command("/etc/init.d/"+serviceName, "enabled").Output(); err == nil {
		info.IsEnabled = true
		info.Properties["enable_method"] = "init_script"
	} else {
		info.IsEnabled = false
	}

	// 检查运行状态
	if output, err := exec.Command("/etc/init.d/"+serviceName, "status").Output(); err == nil {
		status := strings.ToLower(strings.TrimSpace(string(output)))
		if strings.Contains(status, "running") || strings.Contains(status, "active") {
			info.IsRunning = true
		}
		info.Properties["status_output"] = status
	}

	// 检查 UCI 配置
	d.checkUCIConfig(serviceName, info)

	return info, nil
}

// detectSysVInitService 检测 SysV Init 服务
func (d *LinuxServiceDetector) detectSysVInitService(ctx DetectionContext, serviceName string, info *ServiceInfo) (*ServiceInfo, error) {
	info.Type = "sysvinit"

	// 检查初始化脚本
	initScriptPath := fmt.Sprintf("/etc/init.d/%s", serviceName)
	if _, err := os.Stat(initScriptPath); err == nil {
		info.IsInstalled = true
		info.ServicePath = initScriptPath
	} else {
		info.IsInstalled = false
		info.ErrorMsg = "Init script not found"
		return info, nil
	}

	// 检查启用状态（通过符号链接）
	runlevels := []string{"2", "3", "4", "5"}
	for _, level := range runlevels {
		linkPath := fmt.Sprintf("/etc/rc%s.d/S??%s", level, serviceName)
		if matches, err := filepath.Glob(linkPath); err == nil && len(matches) > 0 {
			info.IsEnabled = true
			break
		}
	}

	// 检查运行状态
	if output, err := exec.Command("/etc/init.d/"+serviceName, "status").Output(); err == nil {
		status := strings.ToLower(string(output))
		if strings.Contains(status, "running") || strings.Contains(status, "active") {
			info.IsRunning = true
		}
		info.Properties["status_output"] = string(output)
	}

	return info, nil
}

// detectOpenRCService 检测 OpenRC 服务
func (d *LinuxServiceDetector) detectOpenRCService(ctx DetectionContext, serviceName string, info *ServiceInfo) (*ServiceInfo, error) {
	info.Type = "openrc"

	// 检查服务脚本
	initScriptPath := fmt.Sprintf("/etc/init.d/%s", serviceName)
	if _, err := os.Stat(initScriptPath); err == nil {
		info.IsInstalled = true
		info.ServicePath = initScriptPath
	} else {
		info.IsInstalled = false
		info.ErrorMsg = "Init script not found"
		return info, nil
	}

	// 检查启用状态
	if output, err := exec.Command("rc-config", "list").Output(); err == nil {
		if strings.Contains(string(output), serviceName) {
			info.IsEnabled = true
		}
	}

	// 检查运行状态
	if output, err := exec.Command("rc-service", serviceName, "status").Output(); err == nil {
		status := strings.ToLower(string(output))
		if strings.Contains(status, "started") || strings.Contains(status, "running") {
			info.IsRunning = true
		}
		info.Properties["status_output"] = string(output)
	}

	return info, nil
}

// detectGenericService 检测通用服务
func (d *LinuxServiceDetector) detectGenericService(ctx DetectionContext, serviceName string, info *ServiceInfo) (*ServiceInfo, error) {
	info.Type = "generic"

	// 检查常见位置的可执行文件
	executablePaths := []string{
		fmt.Sprintf("/usr/bin/%s", serviceName),
		fmt.Sprintf("/usr/sbin/%s", serviceName),
		fmt.Sprintf("/bin/%s", serviceName),
		fmt.Sprintf("/sbin/%s", serviceName),
		fmt.Sprintf("/usr/local/bin/%s", serviceName),
		fmt.Sprintf("/usr/local/sbin/%s", serviceName),
	}

	for _, path := range executablePaths {
		if _, err := os.Stat(path); err == nil {
			info.IsInstalled = true
			info.ExecutablePath = path
			break
		}
	}

	if !info.IsInstalled {
		// 尝试在 PATH 中查找
		if _, err := exec.LookPath(serviceName); err == nil {
			info.IsInstalled = true
			if path, err := exec.Command("which", serviceName).Output(); err == nil {
				info.ExecutablePath = strings.TrimSpace(string(path))
			}
		}
	}

	// 检查进程是否正在运行
	if output, err := exec.Command("pgrep", "-f", serviceName).Output(); err == nil {
		pids := strings.TrimSpace(string(output))
		if pids != "" {
			info.IsRunning = true
			info.Properties["pids"] = pids
		}
	}

	return info, nil
}

// parseSystemdServiceFile 解析 systemd 服务文件
func (d *LinuxServiceDetector) parseSystemdServiceFile(content string, info *ServiceInfo) {
	lines := strings.Split(content, "\n")
	currentSection := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// 解析节
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.Trim(line, "[]")
			continue
		}

		// 解析键值对
		if strings.Contains(line, "=") && !strings.HasPrefix(line, "#") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}

			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])

			switch currentSection {
			case "Service":
				switch key {
				case "ExecStart":
					info.ExecutablePath = d.extractExecutablePath(value)
				case "WorkingDirectory":
					info.WorkingDir = value
				case "User":
					info.ServiceUser = value
				}
			}

			// 保存所有配置到属性中
			info.Properties[fmt.Sprintf("%s.%s", currentSection, key)] = value
		}
	}
}

// parseSystemdStatus 解析 systemd 状态输出
func (d *LinuxServiceDetector) parseSystemdStatus(output string, info *ServiceInfo) {
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.Contains(line, "Loaded:") {
			info.Properties["loaded_info"] = line
		} else if strings.Contains(line, "Active:") {
			info.Properties["active_info"] = line
		} else if strings.Contains(line, "Main PID:") {
			info.Properties["main_pid"] = line
		} else if strings.Contains(line, "Memory:") {
			info.Properties["memory_usage"] = line
		}
	}
}

// parseProcdInitScript 解析 procd 初始化脚本
func (d *LinuxServiceDetector) parseProcdInitScript(content string, info *ServiceInfo) {
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// 查找可执行文件路径
		if strings.Contains(line, "procd_set_param command") {
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "command" && i+1 < len(parts) {
					info.ExecutablePath = parts[i+1]
					break
				}
			}
		}

		// 查找配置文件
		if strings.Contains(line, "config_load") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				configName := parts[1]
				info.ConfigPath = fmt.Sprintf("/etc/config/%s", configName)
			}
		}
	}
}

// checkSystemdConfigConsistency 检查 systemd 配置一致性
func (d *LinuxServiceDetector) checkSystemdConfigConsistency(serviceName string, info *ServiceInfo) {
	info.ConfigConsistency = true

	// 检查服务文件是否存在可执行文件
	if info.ExecutablePath != "" {
		if _, err := os.Stat(info.ExecutablePath); err != nil {
			info.ConfigConsistency = false
			info.ErrorMsg = fmt.Sprintf("Executable not found: %s", info.ExecutablePath)
		}
	}

	// 检查工作目录是否存在
	if info.WorkingDir != "" {
		if _, err := os.Stat(info.WorkingDir); err != nil {
			info.ConfigConsistency = false
			if info.ErrorMsg != "" {
				info.ErrorMsg += "; "
			}
			info.ErrorMsg += fmt.Sprintf("Working directory not found: %s", info.WorkingDir)
		}
	}
}

// checkUCIConfig 检查 UCI 配置
func (d *LinuxServiceDetector) checkUCIConfig(serviceName string, info *ServiceInfo) {
	uciConfigPath := fmt.Sprintf("/etc/config/%s", serviceName)
	if _, err := os.Stat(uciConfigPath); err == nil {
		info.ConfigPath = uciConfigPath

		// 检查配置内容
		if output, err := exec.Command("uci", "show", serviceName).Output(); err == nil {
			configLines := strings.Split(string(output), "\n")
			info.Properties["uci_config_lines"] = fmt.Sprintf("%d", len(configLines))
		}
	}
}

// extractExecutablePath 从 ExecStart 中提取可执行文件路径
func (d *LinuxServiceDetector) extractExecutablePath(execStart string) string {
	// 移除前缀符号
	execStart = strings.TrimPrefix(execStart, "-")
	execStart = strings.TrimPrefix(execStart, "@")
	execStart = strings.TrimPrefix(execStart, "+")
	execStart = strings.TrimPrefix(execStart, "!")

	// 提取第一个参数作为可执行文件路径
	parts := strings.Fields(execStart)
	if len(parts) > 0 {
		return parts[0]
	}

	return execStart
}

// isCommandAvailable 检查命令是否可用
func (d *LinuxServiceDetector) isCommandAvailable(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}
