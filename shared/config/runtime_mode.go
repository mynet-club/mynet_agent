package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

const RuntimePC = "pc"
const RuntimeGW = "gw"

// 运行形态独立于身份来源 local/online；旧 Linux/BSD 未配置时保留 GW 行为。
func ResolveRuntimeMode(osName string, openwrt bool, selected string) (string, error) {
	if selected != "" && selected != RuntimePC && selected != RuntimeGW {
		return "", fmt.Errorf("runtime mode must be pc or gw")
	}
	if openwrt || osName == "openwrt" {
		if selected == RuntimePC {
			return "", fmt.Errorf("OpenWrt only supports gw mode")
		}
		return RuntimeGW, nil
	}
	switch osName {
	case "darwin", "windows":
		if selected == RuntimeGW {
			return "", fmt.Errorf("%s only supports pc mode", osName)
		}
		return RuntimePC, nil
	case "linux", "freebsd", "openbsd", "netbsd":
		if selected == "" {
			return RuntimeGW, nil
		}
		return selected, nil
	default:
		return "", fmt.Errorf("runtime mode unsupported on %s", osName)
	}
}

func RuntimeModePath() string { return filepath.Join(utils.ConfDir(), "runtime-mode.json") }
func LoadRuntimeMode(osName string, openwrt bool) (string, error) {
	if openwrt || osName == "openwrt" || osName == "darwin" || osName == "windows" {
		return ResolveRuntimeMode(osName, openwrt, "")
	}
	raw, err := os.ReadFile(RuntimeModePath())
	if os.IsNotExist(err) {
		return ResolveRuntimeMode(osName, openwrt, "")
	}
	if err != nil {
		return "", err
	}
	var state struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return "", err
	}
	if state.Mode == "" {
		return "", fmt.Errorf("runtime mode file has no mode")
	}
	return ResolveRuntimeMode(osName, openwrt, state.Mode)
}
func SaveRuntimeMode(osName string, openwrt bool, mode string) error {
	value, err := ResolveRuntimeMode(osName, openwrt, mode)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(struct {
		Mode string `json:"mode"`
	}{value})
	if err != nil {
		return err
	}
	p := RuntimeModePath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(p+".tmp", raw, 0600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}
