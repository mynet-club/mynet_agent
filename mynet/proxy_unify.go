package mynet

// 双轨合一：ProxyIntentState 是唯一真相；PluginProxyConfig 是投影。
//
// 此前两套各写各的：
//   - WebUI/云端 → proxy_intent.json
//   - mynet plugin enable/exits → plugins/proxy.json
// 导致「页面开了、plugin status 仍是关」。
//
// 合并后：
//   - 所有写入先更新 Effective（intent），再 SyncPluginProxy 投影到插件壳
//   - 读取一律以 Effective 为准；插件壳仅供旧路径/诊断

import (
	"fmt"
	"time"

	"github.com/mynet-club/mynet_agent-V2/shared/config"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
	"github.com/mynet-club/mynet_agent-V2/shared/utils"
)

// ProxyStateDir 双控状态目录（与 localapi 同源）。
func ProxyStateDir() string {
	return utils.LocalConfDir()
}

// LoadProxyState 读双控状态。
func LoadProxyState() (*domain.ProxyIntentState, error) {
	return config.LoadProxyIntent(ProxyStateDir())
}

// SaveProxyState 写双控状态。
func SaveProxyState(st *domain.ProxyIntentState) error {
	return config.SaveProxyIntent(ProxyStateDir(), st)
}

// EffectiveProxyPolicy 当前生效策略（可能 nil）。
func EffectiveProxyPolicy() *domain.ProxyPolicy {
	st, err := LoadProxyState()
	if err != nil {
		return nil
	}
	eff := st.Effective()
	if eff == nil {
		return nil
	}
	p := eff.Policy
	return &p
}

// SyncPluginProxyFromPolicy 把生效策略投影到 plugins/proxy.json（插件壳）。
// 保持旧链（enrichIntentWithProxy / plugin apply）能读到同一开关与出口。
func SyncPluginProxyFromPolicy(p *domain.ProxyPolicy) error {
	if p == nil {
		return nil
	}
	base, store, err := activePluginBase()
	if err != nil {
		return err
	}
	pc, err := loadPluginAt(base, domain.PluginNameProxy)
	if err != nil {
		def := domain.NewPluginProxy()
		pc = &def
	}
	pc.Enabled = p.Enabled
	raw, err := domain.DecodePluginProxy(pc)
	if err != nil {
		raw = &domain.PluginProxyConfig{}
	}
	// 出口列表：intent 有则以 intent 为准；intent 空则保留插件已有（避免 enable 清掉 exits）
	if len(p.Exits) > 0 {
		var exits []domain.ProxyExit
		for _, e := range p.Exits {
			if e.NodeID == "" {
				continue
			}
			exits = append(exits, e)
		}
		raw.Exits = exits
		raw.MaxExits = len(exits)
	}
	body, err := domain.EncodePluginProxyBody(raw)
	if err != nil {
		return err
	}
	pc.Config = body
	return store.SavePlugin(base, pc)
}

// SetProxyEnabled 合并写入：intent + 插件投影。
func SetProxyEnabled(on bool) error {
	st, err := LoadProxyState()
	if err != nil {
		return err
	}
	var p domain.ProxyPolicy
	if eff := st.Effective(); eff != nil {
		p = eff.Policy
	}
	// intent 尚无出口时，从插件壳继承一次，避免 enable 清空
	if len(p.Exits) == 0 {
		if base, _, err2 := activePluginBase(); err2 == nil {
			if pc2, err3 := loadPluginAt(base, domain.PluginNameProxy); err3 == nil {
				if c2, err4 := domain.DecodePluginProxy(pc2); err4 == nil {
					for _, e := range c2.EffectiveExits() {
						p.Exits = append(p.Exits, e)
					}
				}
			}
		}
	}
	p.Enabled = on
	if p.Role == "" {
		p.Role = "client"
	}
	if p.Region == "" {
		p.Region = domain.ProxyRegionDomestic
	}
	if p.Policy == "" {
		p.Policy = domain.ProxyPolicyWhitelist
	}
	if p.Split == nil {
		src, mode, derr := domain.ResolveProxySplit(p.Region, p.Policy)
		if derr == nil {
			p.Split = &domain.ProxySplit{ListSource: src, MatchMode: mode}
		}
	}
	now := time.Now()
	if err := st.ApplyLocal(p, now); err != nil {
		return err
	}
	if err := SaveProxyState(st); err != nil {
		return err
	}
	return SyncPluginProxyFromPolicy(&p)
}

// SetProxyExits 合并写入出口列表。
func SetProxyExits(exits []domain.ProxyExit) error {
	st, err := LoadProxyState()
	if err != nil {
		return err
	}
	var p domain.ProxyPolicy
	if eff := st.Effective(); eff != nil {
		p = eff.Policy
	}
	p.Exits = exits
	if err := st.ApplyLocal(p, time.Now()); err != nil {
		return err
	}
	if err := SaveProxyState(st); err != nil {
		return err
	}
	return SyncPluginProxyFromPolicy(&p)
}

// DescribeProxyUnified 一行统一状态（给 status / 诊断）。
func DescribeProxyUnified() string {
	st, err := LoadProxyState()
	if err != nil {
		return "intent: " + err.Error()
	}
	return st.Describe()
}

// EnsurePluginSynced 启动/apply 时把 intent 投影到插件壳（幂等）。
func EnsurePluginSynced() {
	p := EffectiveProxyPolicy()
	if p == nil {
		return
	}
	if err := SyncPluginProxyFromPolicy(p); err != nil {
		fmt.Println("proxy plugin sync:", err)
	}
}
