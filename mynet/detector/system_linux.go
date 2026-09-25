package detector

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// safeExecCommand 安全执行命令
// 捕获所有可能的 panic (包括系统调用错误)
func safeExecCommand(name string, args ...string) (output []byte, err error) {
	// 使用 defer+recover 捕获任何 panic
	defer func() {
		if r := recover(); r != nil {
			// 将 panic 转换为 error
			err = &exec.Error{
				Name: name,
				Err:  os.ErrNotExist, // 假设是命令不存在
			}
			output = nil
		}
	}()

	// 先解析成绝对路径 —— exec.Command 的查找用的是**父进程** PATH，
	// 下面设 cmd.Env 救不了查找（见 syscmd.go 的注释）
	if p := lookPathSys(name); p != "" {
		name = p
	}
	cmd := exec.Command(name, args...)
	// 设置完整的 PATH
	if cmd.Env == nil {
		cmd.Env = append(os.Environ(),
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		)
	}
	return cmd.Output()
}

// LinuxSystemDetector Linux 系统检测器
type LinuxSystemDetector struct {
	osRelease   map[string]string
	releaseInfo map[string]string
}

// NewLinuxSystemDetector 创建 Linux 系统检测器
func NewLinuxSystemDetector() *LinuxSystemDetector {
	detector := &LinuxSystemDetector{
		osRelease:   make(map[string]string),
		releaseInfo: make(map[string]string),
	}

	// 预加载系统信息
	detector.loadOSRelease()
	detector.loadReleaseInfo()

	return detector
}

// DetectSystem 检测系统信息
func (d *LinuxSystemDetector) DetectSystem(ctx DetectionContext) (*SystemInfo, error) {
	info := &SystemInfo{
		OS:           runtime.GOOS,
		Architecture: runtime.GOARCH,
		Properties:   make(map[string]string),
	}

	// 检测发行版和版本
	d.detectDistribution(info)

	// 检测内核版本
	d.detectKernel(info)

	// 检测主机名
	d.detectHostname(info)

	// 检测系统运行时间
	d.detectUptime(info)

	// 检测额外属性
	d.detectProperties(info)

	return info, nil
}

// GetCacheKey 获取缓存键
func (d *LinuxSystemDetector) GetCacheKey() string {
	return "system_linux"
}

// IsSupported 检查是否支持当前系统
func (d *LinuxSystemDetector) IsSupported() bool {
	return runtime.GOOS == "linux"
}

// loadOSRelease 加载 /etc/os-release 信息
func (d *LinuxSystemDetector) loadOSRelease() {
	file, err := os.Open("/etc/os-release")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]
		value := strings.Trim(parts[1], `"`)
		d.osRelease[key] = value
	}
}

// loadReleaseInfo 加载发行版信息
func (d *LinuxSystemDetector) loadReleaseInfo() {
	// 尝试各种release文件
	releaseFiles := []string{
		"/etc/lsb-release",
		"/etc/redhat-release",
		"/etc/debian_version",
		"/etc/alpine-release",
		"/etc/arch-release",
	}

	for _, file := range releaseFiles {
		if content, err := os.ReadFile(file); err == nil {
			d.releaseInfo[file] = strings.TrimSpace(string(content))
		}
	}
}

// detectDistribution 检测发行版
func (d *LinuxSystemDetector) detectDistribution(info *SystemInfo) {
	// 优先使用 os-release
	if name := d.osRelease["NAME"]; name != "" {
		info.Distribution = name
	} else if name := d.osRelease["ID"]; name != "" {
		info.Distribution = name
	}

	// 检测版本
	if version := d.osRelease["VERSION"]; version != "" {
		info.Version = version
	} else if version := d.osRelease["VERSION_ID"]; version != "" {
		info.Version = version
	}

	// 特殊处理 OpenWrt
	if strings.Contains(info.Distribution, "OpenWrt") ||
		strings.Contains(strings.ToLower(info.Distribution), "openwrt") {
		info.OS = "openwrt"
		info.Distribution = "OpenWrt"

		// 从 /etc/openwrt_release 获取更详细信息
		if content, err := os.ReadFile("/etc/openwrt_release"); err == nil {
			lines := strings.Split(string(content), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "DISTRIB_RELEASE=") {
					info.Version = strings.Trim(strings.TrimPrefix(line, "DISTRIB_RELEASE="), `"'`)
				} else if strings.HasPrefix(line, "DISTRIB_DESCRIPTION=") {
					desc := strings.Trim(strings.TrimPrefix(line, "DISTRIB_DESCRIPTION="), `"'`)
					info.Properties["description"] = desc
				}
			}
		}
	}

	// 如果仍未检测到，尝试从其他文件推断
	if info.Distribution == "" {
		d.fallbackDistributionDetection(info)
	}
}

// fallbackDistributionDetection 后备发行版检测
func (d *LinuxSystemDetector) fallbackDistributionDetection(info *SystemInfo) {
	// 检查各种特殊文件
	if _, err := os.Stat("/etc/debian_version"); err == nil {
		info.Distribution = "Debian"
		if version := d.releaseInfo["/etc/debian_version"]; version != "" {
			info.Version = version
		}
	} else if content := d.releaseInfo["/etc/redhat-release"]; content != "" {
		if strings.Contains(content, "CentOS") {
			info.Distribution = "CentOS"
		} else if strings.Contains(content, "Red Hat") {
			info.Distribution = "RHEL"
		} else if strings.Contains(content, "Fedora") {
			info.Distribution = "Fedora"
		}
		// 提取版本号
		words := strings.Fields(content)
		for i, word := range words {
			if strings.Contains(word, ".") && i > 0 {
				info.Version = word
				break
			}
		}
	} else if _, err := os.Stat("/etc/alpine-release"); err == nil {
		info.Distribution = "Alpine"
		if version := d.releaseInfo["/etc/alpine-release"]; version != "" {
			info.Version = version
		}
	} else if _, err := os.Stat("/etc/arch-release"); err == nil {
		info.Distribution = "Arch"
	}
}

// detectKernel 检测内核版本
func (d *LinuxSystemDetector) detectKernel(info *SystemInfo) {
	// 使用安全的命令执行
	if output, err := safeExecCommand("uname", "-r"); err == nil {
		info.Kernel = strings.TrimSpace(string(output))
		return
	}

	// 如果 uname 不存在,尝试从 /proc/version 读取
	if content, err := os.ReadFile("/proc/version"); err == nil {
		// 提取内核版本 (通常格式: Linux version 5.10.x ...)
		if fields := strings.Fields(string(content)); len(fields) >= 3 {
			info.Kernel = fields[2]
		}
	}
}

// detectHostname 检测主机名
func (d *LinuxSystemDetector) detectHostname(info *SystemInfo) {
	if hostname, err := os.Hostname(); err == nil {
		info.Hostname = hostname
	}
}

// detectUptime 检测系统运行时间
func (d *LinuxSystemDetector) detectUptime(info *SystemInfo) {
	if content, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(content))
		if len(fields) > 0 {
			if seconds, err := strconv.ParseFloat(fields[0], 64); err == nil {
				info.Uptime = time.Duration(seconds) * time.Second
			}
		}
	}
}

// detectProperties 检测额外属性
func (d *LinuxSystemDetector) detectProperties(info *SystemInfo) {
	// CPU 信息
	if content, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "model name") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					info.Properties["cpu_model"] = strings.TrimSpace(parts[1])
					break
				}
			}
		}
	}

	// 内存信息
	if content, err := os.ReadFile("/proc/meminfo"); err == nil {
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "MemTotal:") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					info.Properties["total_memory"] = parts[1] + " kB"
					break
				}
			}
		}
	}

	// 负载平均值
	if content, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(content))
		if len(fields) >= 3 {
			info.Properties["load_avg_1m"] = fields[0]
			info.Properties["load_avg_5m"] = fields[1]
			info.Properties["load_avg_15m"] = fields[2]
		}
	}

	// 从 os-release 复制有用的信息
	for key, value := range d.osRelease {
		switch key {
		case "PRETTY_NAME", "BUILD_ID", "VARIANT", "VARIANT_ID":
			info.Properties[strings.ToLower(key)] = value
		}
	}

	// 检测容器环境
	if d.isInContainer() {
		info.Properties["container"] = "true"
		if containerType := d.detectContainerType(); containerType != "" {
			info.Properties["container_type"] = containerType
		}
	}

	// 检测虚拟化
	if virt := d.detectVirtualization(); virt != "" {
		info.Properties["virtualization"] = virt
	}
}

// isInContainer 检测是否在容器中
func (d *LinuxSystemDetector) isInContainer() bool {
	// 检查 /.dockerenv
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}

	// 检查 /proc/1/cgroup
	if content, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		if strings.Contains(string(content), "docker") ||
			strings.Contains(string(content), "lxc") ||
			strings.Contains(string(content), "containerd") {
			return true
		}
	}

	return false
}

// detectContainerType 检测容器类型
func (d *LinuxSystemDetector) detectContainerType() string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}

	if content, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		contentStr := string(content)
		if strings.Contains(contentStr, "docker") {
			return "docker"
		} else if strings.Contains(contentStr, "lxc") {
			return "lxc"
		} else if strings.Contains(contentStr, "containerd") {
			return "containerd"
		}
	}

	return ""
}

// detectVirtualization 检测虚拟化环境
func (d *LinuxSystemDetector) detectVirtualization() string {
	// 尝试 systemd-detect-virt (使用安全执行)
	if output, err := safeExecCommand("systemd-detect-virt"); err == nil {
		virt := strings.TrimSpace(string(output))
		if virt != "none" {
			return virt
		}
	}

	// 检查 DMI 信息
	dmiPaths := []string{
		"/sys/class/dmi/id/product_name",
		"/sys/class/dmi/id/sys_vendor",
		"/sys/class/dmi/id/board_vendor",
	}

	for _, path := range dmiPaths {
		if content, err := os.ReadFile(path); err == nil {
			vendor := strings.ToLower(strings.TrimSpace(string(content)))
			switch {
			case strings.Contains(vendor, "vmware"):
				return "vmware"
			case strings.Contains(vendor, "virtualbox"):
				return "virtualbox"
			case strings.Contains(vendor, "qemu"):
				return "qemu"
			case strings.Contains(vendor, "kvm"):
				return "kvm"
			case strings.Contains(vendor, "xen"):
				return "xen"
			case strings.Contains(vendor, "microsoft") && strings.Contains(vendor, "virtual"):
				return "hyperv"
			}
		}
	}

	return ""
}
