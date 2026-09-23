package utils

// 跨平台路径解析：env 覆盖 > MYNET_HOME/可执行文件推断 > 平台默认。
// 隐性知识：sudo 下优先用原始用户 home（SUDO_USER），并复用已存在的 ~/.mynet。

import (
	"os"
	"path/filepath"
	"runtime"
)

const (
	envHome       = "MYNET_HOME"
	envConfigHome = "MYNET_CONFIG_HOME"
	envDataHome   = "MYNET_DATA_HOME"
	envLogDir     = "MYNET_LOG_DIR"
)

// GetMyNetHome 安装/数据根目录。
// 优先级: MYNET_HOME > 可执行文件路径推断（…/bin/mynet → …）> 当前目录
func GetMyNetHome() string {
	if mynetHome := os.Getenv(envHome); mynetHome != "" {
		return mynetHome
	}
	if exePath, err := os.Executable(); err == nil {
		if realPath, err := filepath.EvalSymlinks(exePath); err == nil {
			exePath = realPath
		}
		exeDir := filepath.Dir(exePath)
		if filepath.Base(exeDir) == "bin" {
			return filepath.Dir(exeDir)
		}
		return exeDir
	}
	return "."
}

func dirExists(p string) bool {
	if fi, err := os.Stat(p); err == nil {
		return fi.IsDir()
	}
	return false
}

func fileExists(p string) bool {
	if fi, err := os.Stat(p); err == nil {
		return !fi.IsDir()
	}
	return false
}

func getSudoUserHome() string {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && os.Getuid() == 0 {
		switch runtime.GOOS {
		case "darwin":
			return filepath.Join("/Users", sudoUser)
		default:
			return filepath.Join("/home", sudoUser)
		}
	}
	if home, _ := os.UserHomeDir(); home != "" {
		return home
	}
	return ""
}

// UserBaseDir 用户状态/配置基目录。
// 优先级: MYNET_HOME > root 系统目录 > legacy ~/.mynet > 平台默认
func UserBaseDir() string {
	if v := os.Getenv(envHome); v != "" {
		_ = SafeCreateDir(v, 0o700)
		return v
	}
	if os.Getuid() == 0 {
		switch runtime.GOOS {
		case "darwin":
			d := "/usr/local/var/lib/mynet"
			_ = SafeCreateDir(d, 0o700)
			return d
		default:
			d := "/var/lib/mynet"
			_ = SafeCreateDir(d, 0o700)
			return d
		}
	}
	home := getSudoUserHome()
	if home == "" {
		if h, _ := os.UserHomeDir(); h != "" {
			home = h
		}
	}
	if home != "" {
		legacy := filepath.Join(home, ".mynet")
		if dirExists(legacy) || fileExists(filepath.Join(legacy, "config.json")) || fileExists(filepath.Join(legacy, "storage.json")) {
			_ = SafeCreateDir(legacy, 0o700)
			return legacy
		}
	}
	if home != "" {
		switch runtime.GOOS {
		case "darwin":
			d := filepath.Join(home, "Library", "Application Support", "MyNet")
			_ = SafeCreateDir(d, 0o700)
			return d
		case "windows":
			d := filepath.Join(home, "AppData", "Local", "MyNet")
			_ = SafeCreateDir(d, 0o700)
			return d
		default:
			if v := os.Getenv("XDG_STATE_HOME"); v != "" {
				d := filepath.Join(v, "mynet")
				_ = SafeCreateDir(d, 0o700)
				return d
			}
			d := filepath.Join(home, ".local", "state", "mynet")
			_ = SafeCreateDir(d, 0o700)
			return d
		}
	}
	d := ".mynet"
	_ = SafeCreateDir(d, 0o700)
	return d
}

// ConfigDir 配置目录。优先级: MYNET_CONFIG_HOME > UserBaseDir
func ConfigDir() string {
	if v := os.Getenv(envConfigHome); v != "" {
		_ = SafeCreateDir(v, 0o700)
		return v
	}
	return UserBaseDir()
}

// DataDir 可变状态目录。优先级: MYNET_DATA_HOME > UserBaseDir
func DataDir() string {
	if v := os.Getenv(envDataHome); v != "" {
		_ = SafeCreateDir(v, 0o700)
		return v
	}
	return UserBaseDir()
}

// LogsDir 日志目录。优先级: MYNET_LOG_DIR > UserBaseDir/logs
func LogsDir() string {
	if v := os.Getenv(envLogDir); v != "" {
		_ = SafeCreateDir(v, 0o755)
		return v
	}
	d := filepath.Join(UserBaseDir(), "logs")
	_ = SafeCreateDir(d, 0o755)
	return d
}

// NodesDir 每节点运行时元数据
func NodesDir() string {
	d := filepath.Join(DataDir(), "nodes")
	_ = SafeCreateDir(d, 0o700)
	return d
}

// SafeCreateDir 创建目录（shared 层不做 chown——属系统能力，归执行器）
func SafeCreateDir(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}
