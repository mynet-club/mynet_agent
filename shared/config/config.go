package config

import (
	"encoding/json"
	"fmt"
	"github.com/mynet-club/mynet_agent-V2/shared/utils"
	"os"
	"path/filepath"
	"strings"
)

// Config represents the client configuration
type Config struct {
	Server          string   `json:"server"`
	Email           string   `json:"email"`
	LastEmail       string   `json:"last_email,omitempty"` // 记录最后登录邮箱，即使Token失效也保留
	Token           string   `json:"token"`
	RefreshToken    string   `json:"refresh_token,omitempty"`
	TokenType       string   `json:"token_type,omitempty"`
	ExpiresIn       int      `json:"expires_in,omitempty"`
	Region          string   `json:"region,omitempty"`
	UserID          int64    `json:"user_id,omitempty"`
	UserName        string   `json:"user_name,omitempty"`
	CurrentZone     *Zone    `json:"current_zone,omitempty"`
	CurrentNode     string   `json:"current_node,omitempty"`
	CurrentNodeInfo *NodeRef `json:"current_node_info,omitempty"`
	ConfigVersion   int      `json:"config_version,omitempty"` // 配置结构/语义版本，兼容处理用

	// --- F1: UI 可配置字段（后续可在设置页暴露） ---
	UI *UISettings `json:"ui_settings,omitempty"`
}

// UISettings 聚合用户可调 UI / 刷新 / 主题偏好
type UISettings struct {
	// 自动刷新间隔（秒），0 表示使用默认；负数表示禁用（例如 Status 自动刷新）
	StatusAutoRefresh int `json:"status_auto_refresh,omitempty"`
	// 并行刷新开关默认值（true=并行 false=顺序），nil 表示使用应用默认
	ParallelRefresh *bool `json:"parallel_refresh,omitempty"`
	// 主题名称（将来切换颜色方案），留空=默认
	Theme string `json:"theme,omitempty"`
	// 确认对话默认危险再次确认提示开关
	ConfirmDangerHint *bool `json:"confirm_danger_hint,omitempty"`
	// 调试详细错误输出开关（true 显示底层 error/stack，false 显示简化提示）
	DebugVerbose bool `json:"debug_verbose,omitempty"`
	// 自动更新检查开关（true=启用启动时检查更新，false=禁用，nil=使用默认）
	AutoUpdateEnabled *bool `json:"auto_update_enabled,omitempty"`
}

// Zone represents a zone configuration
type Zone struct {
	ID          int    `json:"id"`
	ZoneID      int64  `json:"zone_id"`
	ZoneName    string `json:"zone_name"`
	ZoneDomain  string `json:"zone_domain"`
	ZoneRemarks string `json:"zone_remarks"`
	IsAdmin     bool   `json:"is_admin"`
}

// NodeRef stores a concise snapshot of the chosen node
type NodeRef struct {
	NodeID    int64  `json:"node_id"`
	NodeName  string `json:"node_name"`
	VpnType   string `json:"vpn_type,omitempty"`
	Status    string `json:"status,omitempty"`
	VirtualIP string `json:"virtual_ip,omitempty"`
	ZoneID    int64  `json:"zone_id,omitempty"`
}

// getConfigPath returns the path to the configuration file
func getConfigPath() (string, error) {
	configDir := utils.ConfigDir()
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create config directory: %w", err)
	}
	return filepath.Join(configDir, "config.json"), nil
}

// Load loads the configuration from file
func Load() (*Config, error) {
	configPath, err := getConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("configuration not found, please login first")
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// 回填默认结构
	if config.UI == nil {
		config.UI = &UISettings{}
	}
	return &config, nil
}

// Save saves the configuration to file
func Save(config *Config) error {
	configPath, err := getConfigPath()
	if err != nil {
		return err
	}
	// 若未设置版本，自动赋值当前版本（简单整型，后续递增）
	if config.ConfigVersion == 0 {
		config.ConfigVersion = 1
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// Exists checks if the configuration file exists
func Exists() bool {
	configPath, err := getConfigPath()
	if err != nil {
		return false
	}

	_, err = os.Stat(configPath)
	return err == nil
}

// LoadServerConfig loads server configuration from app config
func LoadServerConfig() (*ServerConfig, error) {
	appConfig, err := LoadAppConfig("conf/config.json")
	if err != nil {
		return nil, fmt.Errorf("failed to load app config: %w", err)
	}

	if appConfig.ServerConfig == nil {
		// Return default configuration instead of localhost
		return &ServerConfig{
			APIBaseURL: "https://api.mynet.example.com/v1",
			Timeout:    30,
			RetryCount: 3,
		}, nil
	}

	return appConfig.ServerConfig, nil
}

// ResolveServerURL 优先级:
// 1) 用户配置中的 Server 字段
// 2) 环境变量 MYNET_API_BASE_URL
// 3) 系统 conf/config.json 中 server_config.api_base_url
// 4) 回退默认
func ResolveServerURL(userCfg *Config) string {
	if userCfg != nil && strings.TrimSpace(userCfg.Server) != "" {
		return strings.TrimSpace(userCfg.Server)
	}
	if env := strings.TrimSpace(os.Getenv("MYNET_API_BASE_URL")); env != "" {
		return env
	}
	if _, err := os.Stat("conf/config.json"); err == nil {
		if appCfg, e2 := LoadAppConfig("conf/config.json"); e2 == nil && appCfg.ServerConfig != nil {
			if s := strings.TrimSpace(appCfg.ServerConfig.APIBaseURL); s != "" {
				return s
			}
		}
	}
	return "https://api.mynet.example.com/v1"
}

// GetLastLoginEmail 获取最后登录的邮箱，即使当前没有有效Token
func GetLastLoginEmail() string {
	cfg, err := Load()
	if err != nil {
		return ""
	}

	// 优先返回当前邮箱（如果有有效登录）
	if cfg.Email != "" && cfg.Token != "" {
		return cfg.Email
	}

	// 否则返回最后登录的邮箱
	return cfg.LastEmail
}

// UpdateLastLoginEmail 更新最后登录的邮箱
func UpdateLastLoginEmail(email string) error {
	cfg, err := Load()
	if err != nil {
		// 如果配置文件不存在，创建一个只包含最后邮箱的配置
		cfg = &Config{
			LastEmail: email,
		}
	} else {
		cfg.LastEmail = email
	}

	return Save(cfg)
}

// ClearTokens 清除当前用户的token信息（保留其他配置）
func ClearTokens() error {
	cfg, err := Load()
	if err != nil {
		return err
	}

	// 保存当前邮箱作为最后登录邮箱
	if cfg.Email != "" {
		cfg.LastEmail = cfg.Email
	}

	// 清除token相关信息
	cfg.Token = ""
	cfg.RefreshToken = ""
	cfg.TokenType = ""
	cfg.ExpiresIn = 0
	cfg.Email = ""
	cfg.UserID = 0
	cfg.UserName = ""

	return Save(cfg)
}
