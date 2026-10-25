// Package proxysync —— 插件壳投影（G3 小包，不引入 detector/tools）。
// 从 mynet.SyncPluginProxyFromPolicy 抽出，供组合根注入，避免 mynetd 传递依赖执行器大包。
package proxysync

import (
	"os"
	"path/filepath"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

func activePluginBase() (string, *config.Store, error) {
	store := &config.Store{}
	src, _, err := store.ActiveSource()
	if err != nil {
		return "", nil, err
	}
	return utils.PluginsConfDir(src), store, nil
}

func loadPluginAt(base, name string) (*domain.PluginConfig, error) {
	raw, err := os.ReadFile(filepath.Join(base, name+".json"))
	if err != nil {
		return nil, err
	}
	return domain.DecodePluginConfig(raw)
}

// LoadPluginProxyConfig 读插件壳里的 proxy 配置本体（含出口列表）。
//
// 为什么需要它：出口（config.exits）在本机环境这一侧，**不进 intent 的 wire**。
// SyncPluginProxyFromPolicy 在 intent 没有 exits 时会保留插件已有值，于是
// 「实际生效的出口」可以和「意图」不一致 —— UI 只读 intent 就会在明明有出口
// 的节点上显示「尚未选择出口设备」。
func LoadPluginProxyConfig() (*domain.PluginProxyConfig, error) {
	base, _, err := activePluginBase()
	if err != nil {
		return nil, err
	}
	pc, err := loadPluginAt(base, domain.PluginNameProxy)
	if err != nil {
		return nil, err
	}
	return domain.DecodePluginProxy(pc)
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
