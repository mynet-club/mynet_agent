package config

import (
	"encoding/json"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"os"
	"path/filepath"
)

// ProxyDomainBaseline 由 ctl 发布、独立于本机策略存储。空列表也有权威性（撤回全部）。
type ProxyDomainBaseline struct {
	Proxied []string `json:"proxied"`
	// Direct 兼容旧快照；PC 白名单加速不再下载或采用基础直连表。
	Direct  []string `json:"direct,omitempty"`
	Version string   `json:"version"`
}

func ProxyDomainBaselinePath(base string) string {
	return filepath.Join(base, "proxy_domain_baseline.json")
}
func LoadProxyDomainBaseline(base string) (*ProxyDomainBaseline, error) {
	raw, err := os.ReadFile(ProxyDomainBaselinePath(base))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var baseline ProxyDomainBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		return nil, err
	}
	return &baseline, nil
}
func SaveProxyDomainBaseline(base string, b *ProxyDomainBaseline) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	p := ProxyDomainBaselinePath(base)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(p+".tmp", raw, 0600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

func applyProxyBaseline(st *domain.ProxyIntentState, b *ProxyDomainBaseline) {
	if st.Local == nil || b == nil {
		return
	}
	if st.Local.Policy.DomainRules == nil {
		st.Local.Policy.DomainRules = &domain.ProxyDomainRules{}
	}
	st.Local.Policy.DomainRules.Proxied = append([]string{}, b.Proxied...)
	st.Local.Policy.DomainRules.Direct = append([]string{}, b.Direct...)
}
