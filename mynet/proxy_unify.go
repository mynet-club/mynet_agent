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
	"reflect"
	"time"

	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/utils"
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

// SetProxyCustomDomains D5：写自定义域名叠加（本机，不回写云端）。
// customProxied 追加到代理组；customDirect 追加到直连组（优先级最高）。
func SetProxyCustomDomains(customProxied, customDirect []string) error {
	st, err := LoadProxyState()
	if err != nil {
		return err
	}
	var p domain.ProxyPolicy
	if eff := st.Effective(); eff != nil {
		p = eff.Policy
	}
	if p.DomainRules == nil {
		p.DomainRules = &domain.ProxyDomainRules{}
	}
	p.DomainRules.CustomDomains = customProxied
	p.DomainRules.CustomDirect = customDirect
	if err := st.ApplyLocal(p, time.Now()); err != nil {
		return err
	}
	if err := SaveProxyState(st); err != nil {
		return err
	}
	return SyncPluginProxyFromPolicy(&p)
}

// StampCloudIntent 把云端 bundle 的 proxy 策略记入 intent 的 Cloud 轨。
//
// 历史断链：读取侧（planFromBundle）只看 bundle、写入侧（ApplyCloudIntent）无人调用，
// 导致「本地页面关掉开关、下一次 config sync 又被云端策略复活」。这里把云端下发
// 落到 Cloud 轨，Effective() 才能按 revision 仲裁本地覆盖。
//
// 防复活两道闸（generation 实际是 "rev-…" 字符串，解析不出数字是常态）：
//  1. Cloud 轨内容与本次下发一致 → 跳过重 stamp。否则会自增 revision 越过
//     本地关掉的那次写入，每次 sync 都复活一次。
//  2. revision 未知（<=0）且本地有未回写（Dirty）的修改 → 无法证明云端更新，
//     保持 Cloud 轨现状，不覆盖。
//
// 校验失败仅告警，返回 false，调用方回落 bundle 原策略。
func StampCloudIntent(p *domain.ProxyPolicy, revision int64) bool {
	if p == nil {
		return false
	}
	st, err := LoadProxyState()
	if err != nil {
		logger.Comp("apply").Warn("proxy intent 读取失败，云端策略未落轨", "err", err)
		return false
	}
	if st.Cloud != nil && reflect.DeepEqual(st.Cloud.Policy, *p) {
		return true // 内容未变：不动 revision
	}
	if revision <= 0 && st.Local != nil && st.Status.Dirty {
		logger.Comp("apply").Info("proxy 云端下发无版本号且本地有未回写修改，保持本地覆盖",
			"local_rev", st.Local.Revision)
		return st.Cloud != nil
	}
	if err := st.ApplyCloud(*p, revision, time.Now()); err != nil {
		logger.Comp("apply").Warn("proxy 云端策略 stamp 失败，回落 bundle", "err", err)
		return false
	}
	if err := SaveProxyState(st); err != nil {
		logger.Comp("apply").Warn("proxy intent 保存失败", "err", err)
		return false
	}
	return true
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
