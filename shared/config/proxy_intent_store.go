package config

// ProxyIntentState 落盘。文件：conf/<source>/proxy_intent.json

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/mynet-club/mynet_agent-V2/shared/domain"
	"github.com/mynet-club/mynet_agent-V2/shared/utils"
)

// ProxyIntentPath 双控状态文件。
func ProxyIntentPath(base string) string {
	return filepath.Join(base, "proxy_intent.json")
}

// LoadProxyIntent 读双控状态；文件不存在返回空状态（非错误）。
func LoadProxyIntent(base string) (*domain.ProxyIntentState, error) {
	raw, err := os.ReadFile(ProxyIntentPath(base))
	if err != nil {
		if os.IsNotExist(err) {
			return &domain.ProxyIntentState{}, nil
		}
		return nil, err
	}
	var st domain.ProxyIntentState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// SaveProxyIntent 写盘（0600）。
func SaveProxyIntent(base string, st *domain.ProxyIntentState) error {
	if err := utils.SafeCreateDir(base, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ProxyIntentPath(base), raw, 0o600)
}
