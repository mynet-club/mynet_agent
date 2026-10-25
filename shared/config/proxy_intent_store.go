package config

// ProxyIntentState 落盘。文件：conf/<source>/proxy_intent.json

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
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
	// 旧状态自愈：LocalOnly 不该挂 dirty（历史 ApplyLocal 无条件标脏）
	st.HealLocalOnlyDirty()
	baseline, err := LoadProxyDomainBaseline(base)
	if err != nil {
		return nil, err
	}
	applyProxyBaseline(&st, baseline)
	return &st, nil
}

// SaveProxyIntent 写盘（0600）。
func SaveProxyIntent(base string, st *domain.ProxyIntentState) error {
	if err := utils.SafeCreateDir(base, 0o700); err != nil {
		return err
	}
	disk := *st
	baseline, err := LoadProxyDomainBaseline(base)
	if err != nil {
		return err
	}
	if baseline != nil && st.Local != nil && st.Local.Policy.DomainRules != nil {
		local := *st.Local
		rules := *local.Policy.DomainRules
		rules.Proxied, rules.Direct = nil, nil
		local.Policy.DomainRules = &rules
		disk.Local = &local
	}
	raw, err := json.MarshalIndent(&disk, "", "  ")
	if err != nil {
		return err
	}
	p := ProxyIntentPath(base)
	if err := os.WriteFile(p+".tmp", raw, 0600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}
