package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AppConfig represents the application configuration structure
type AppConfig struct {
	AppPath      string           `json:"app_path"`
	ServerConfig *ServerConfig    `json:"server_config"`
	Downloads    *DownloadsConfig `json:"downloads,omitempty"`
	Refresh      *RefreshConfig   `json:"refresh,omitempty"`
}

// ServerConfig represents server configuration
type ServerConfig struct {
	APIBaseURL string `json:"api_base_url"`
	Timeout    int    `json:"timeout"`
	RetryCount int    `json:"retry_count"`
}

// DownloadsConfig contains software download sources
type DownloadsConfig struct {
	AppsIndexURL string `json:"apps_index_url"`
}

// RefreshConfig controls UI/data refresh related tuning (TTL seconds etc.)
type RefreshConfig struct {
	ZonesTTLSeconds          int `json:"zones_ttl_seconds,omitempty"`
	NodesTTLSeconds          int `json:"nodes_ttl_seconds,omitempty"`
	StatusTTLSeconds         int `json:"status_ttl_seconds,omitempty"`
	StatusAutoRefreshSeconds int `json:"status_auto_refresh_seconds,omitempty"`
}

// UserConfig represents user configuration
type UserConfig struct {
	UserID       string       `json:"user_id"`
	Email        string       `json:"email"`
	Token        string       `json:"token"`
	RefreshToken string       `json:"refresh_token,omitempty"`
	Name         string       `json:"name"`
	Zones        []ZoneConfig `json:"zones"`
}

// ZoneConfig represents zone configuration
type ZoneConfig struct {
	ZoneID     string       `json:"zone_id"`
	ZoneName   string       `json:"zone_name"`
	Region     string       `json:"region"`
	ServerAddr string       `json:"server_addr"`
	Nodes      []NodeConfig `json:"nodes"`
}

// NodeConfig represents node configuration
type NodeConfig struct {
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name"`
	NodeType   string `json:"node_type"` // "gnb" or "wireguard"
	Status     string `json:"status"`    // "online", "offline", "unknown"
	LocalIP    string `json:"local_ip"`
	PublicIP   string `json:"public_ip"`
	ConfigPath string `json:"config_path"`
	PidFile    string `json:"pid_file"`
	LogFile    string `json:"log_file"`
}

// AppPaths represents application directory structure
type AppPaths struct {
	Root  string
	Bin   string
	Conf  string
	Var   string
	VPN   string
	GNB   string
	WG    string
	Logs  string
	Token string
	PID   string
}

// GetAppPaths returns the application directory structure
func GetAppPaths(rootPath string) *AppPaths {
	if rootPath == "" {
		// Default to current directory or detect from executable
		rootPath = "."
	}

	return &AppPaths{
		Root:  rootPath,
		Bin:   filepath.Join(rootPath, "bin"),
		Conf:  filepath.Join(rootPath, "conf"),
		Var:   filepath.Join(rootPath, "var"),
		VPN:   filepath.Join(rootPath, "driver"),
		GNB:   filepath.Join(rootPath, "driver", "gnb"),
		WG:    filepath.Join(rootPath, "driver", "wireguard"),
		Logs:  filepath.Join(rootPath, "var", "logs"),
		Token: filepath.Join(rootPath, "var", "token"),
		PID:   filepath.Join(rootPath, "var", "pid"),
	}
}

// GetNodeGNBPath returns the GNB configuration path for a specific node
func GetNodeGNBPath(paths *AppPaths, nodeID string) string {
	return filepath.Join(paths.GNB, "conf", nodeID)
}

// GetNodeSecurityPath returns the security files path for a specific node
func GetNodeSecurityPath(paths *AppPaths, nodeID string) string {
	return filepath.Join(paths.GNB, "conf", nodeID, "security")
}

// LoadAppConfig loads the application configuration
func LoadAppConfig(configPath string) (*AppConfig, error) {
	if configPath == "" {
		configPath = filepath.Join("conf", "config.json")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty config if file doesn't exist
			return &AppConfig{
				AppPath: filepath.Dir(configPath),
			}, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config AppConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &config, nil
}

// SaveAppConfig saves the application configuration
func SaveAppConfig(config *AppConfig, configPath string) error {
	if configPath == "" {
		configPath = filepath.Join("conf", "config.json")
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// InitializeDirectories creates the required directory structure
func InitializeDirectories(paths *AppPaths) error {
	dirs := []string{
		paths.Bin,
		paths.Conf,
		paths.Var,
		paths.Logs,
		paths.Token,
		paths.PID,
		paths.VPN,
		paths.GNB,
		paths.WG,
		filepath.Join(paths.GNB, "bin"),
		filepath.Join(paths.GNB, "conf"),
		filepath.Join(paths.GNB, "src"),
		filepath.Join(paths.WG, "bin"),
		filepath.Join(paths.WG, "conf"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return nil
}
