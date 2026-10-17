package mynet

// 隧道启停联动 proxy / DNS 侧车 —— 依赖拓扑与启停编排。
//
// ── 依赖关系（谁靠谁）────────────────────────────────────────
//
//	tunnel（底座）
//	  ├── proxy     平行侧车：出口选点 / 分流投影。只依赖 tunnel 数据面。
//	  └── dns-plan  平行侧车：系统 DNS 三选一（peer VIP 走隧道）。只依赖 tunnel。
//	        └── dns-hook  叠加层：本地加速钩子。
//	                      依赖 tunnel（钉 /32）+ proxy.DomainRules（域名清单）。
//
// 结论：**proxy 与 DNS 是平行关系，不是链式依赖**。
//   - DNS peer 模式只把系统 DNS 指到隧道 VIP，不需要 proxy 在跑。
//   - proxy 按 IP/域名分流，也不要求系统 DNS 被接管。
//   - 只有 dns-hook 同时吃两者（DomainRules 配置 + 隧道口）。
//
// ── 编排：启动分层顺序，停止栈式逆序 ─────────────────────────
//
//	up:    tunnel → [proxy ∥ dns-plan] → dns-hook
//	down:  dns-hook → [dns-restore ∥ proxy-静默] → tunnel
//
// 为什么不用单链强依赖：proxy 失败不应挡住 DNS，反之亦然 ——
// 侧车是增强不是隧道前提，单项失败只告警。为什么 dns-hook 必须最后起/
// 最先停：它把解析结果钉进隧道，隧道口不在时钉扎无意义；停的时候先撤钩子
// 才能把系统 DNS 干净交还 OS。
//
// D4/§14 仍然成立：mode=off/空 **绝不** 动系统 DNS；service start 不做默认劫持。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ── 加速意图（用户偏好，与进程事实分离）────────────────────────

// dnsHookIntentFile 用户「加速」意图（托盘/面板开关写入，隧道重启时读取）。
// 与运行态 dns-hook.pid 分开：pid 是进程事实，这里是用户偏好。
func dnsHookIntentFile() string {
	return filepath.Join(varDir(), "proxy", "dns-hook-intent.json")
}

type dnsHookIntent struct {
	Enabled bool `json:"enabled"`
}

// SetDNSHookWanted 记住加速偏好（托盘/CLI 用户动作）。
func SetDNSHookWanted(on bool) error {
	b, _ := json.MarshalIndent(dnsHookIntent{Enabled: on}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(dnsHookIntentFile()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dnsHookIntentFile(), append(b, '\n'), 0o644)
}

// DNSHookWanted 用户是否开过加速。文件缺失 = 未启用（默认不劫持）。
func DNSHookWanted() bool {
	raw, err := os.ReadFile(dnsHookIntentFile())
	if err != nil {
		return false
	}
	var it dnsHookIntent
	if json.Unmarshal(raw, &it) != nil {
		return false
	}
	return it.Enabled
}

// dnsHookRunning dns-hook 进程是否存活（只看事实，不看意图）。
func dnsHookRunning() bool {
	pid := readHookPID()
	return pid > 0 && processAlive(pid)
}

// ── 编排入口 ─────────────────────────────────────────────────

// sidecarResult 单项结果：编排层据此打日志，不中断兄弟侧车。
type sidecarResult struct {
	name string
	err  error
}

func (r sidecarResult) log() {
	if r.err != nil {
		fmt.Fprintf(os.Stderr, "⚠ %s: %v\n", r.name, r.err)
		return
	}
	fmt.Printf("%s: ok\n", r.name)
}

// syncProxySidecar proxy 侧：intent 开着就重投影插件壳（幂等）。
// 与 DNS 无依赖 —— 即使 DNS 失败也应执行完。
func syncProxySidecar() sidecarResult {
	p := EffectiveProxyPolicy()
	if p == nil || !p.Enabled {
		return sidecarResult{name: "proxy", err: nil} // 未启用 = 跳过，不是错误
	}
	EnsurePluginSynced()
	if err := ApplyPlugins(false); err != nil {
		return sidecarResult{name: "proxy", err: err}
	}
	return sidecarResult{name: "proxy"}
}

// syncDNSPlanSidecar DNS 三选一侧：只在用户配了 peer/smart 时落地系统 DNS。
// off/空不碰（D4/§14）。与 proxy 无依赖。
func syncDNSPlanSidecar() sidecarResult {
	p := EffectiveProxyPolicy()
	if p == nil || p.DNS == nil || !p.DNS.Active() {
		return sidecarResult{name: "dns-plan", err: nil} // 未启用 = 跳过
	}
	if err := ApplyProxyDNSPlan("", p.DNS); err != nil {
		return sidecarResult{name: "dns-plan", err: err}
	}
	return sidecarResult{name: "dns-plan"}
}

// startDNSHookSidecar 加速钩子（叠加层）：意图开着且隧道已在，才拉起。
// 依赖：tunnel（钉扎目标）+ DomainRules（清单，随 EffectiveProxyPolicy 读）。
func startDNSHookSidecar() sidecarResult {
	if !DNSHookWanted() {
		return sidecarResult{name: "dns-hook", err: nil} // 用户没开 = 跳过
	}
	if dnsHookRunning() {
		return sidecarResult{name: "dns-hook"} // 已在跑 = 幂等成功
	}
	if rc := dnsHookStart(DNSHookListenDefault); rc != 0 {
		return sidecarResult{name: "dns-hook", err: fmt.Errorf("exit %d", rc)}
	}
	return sidecarResult{name: "dns-hook"}
}

// stopDNSHookSidecar 停钩子（还原它改过的系统 DNS）。
// **不改**用户意图（DNSHookWanted 保持），下次隧道起来还能自动跟上。
func stopDNSHookSidecar() sidecarResult {
	if !dnsHookRunning() {
		return sidecarResult{name: "dns-hook", err: nil}
	}
	if rc := dnsHookStop(); rc != 0 {
		return sidecarResult{name: "dns-hook", err: fmt.Errorf("exit %d", rc)}
	}
	return sidecarResult{name: "dns-hook"}
}

// restoreDNSSidecar 幂等还原系统 DNS（历史接管 / 三选一落地过的都收）。
func restoreDNSSidecar() sidecarResult {
	if !isPCDNSPlatform() {
		return sidecarResult{name: "dns-restore", err: nil}
	}
	if err := RestoreSystemDNS(); err != nil {
		return sidecarResult{name: "dns-restore", err: err}
	}
	return sidecarResult{name: "dns-restore"}
}

// SyncSidecarsOnTunnelUp 隧道起来后的侧车编排。
//
//	第 1 层（平行，互不阻塞）：proxy ∥ dns-plan
//	第 2 层（叠加）：dns-hook
//
// 单项失败只打 ⚠，不回滚隧道 —— 侧车是增强，不是隧道前提。
func SyncSidecarsOnTunnelUp() {
	// 层内平行：刻意先 proxy 后 dns-plan 只为日志顺序稳定；
	// 二者无数据依赖，任一失败不影响另一个。
	syncProxySidecar().log()
	syncDNSPlanSidecar().log()
	// 叠加层最后：需要隧道口 + DomainRules 都已就绪。
	startDNSHookSidecar().log()
}

// SyncSidecarsOnTunnelDown 隧道停止前的侧车编排（栈式逆序）。
//
//	dns-hook → dns-restore →（proxy 静默，路由随隧道口消失）→ tunnel
func SyncSidecarsOnTunnelDown() {
	stopDNSHookSidecar().log()
	restoreDNSSidecar().log()
	// proxy 无需显式停：分流路由挂在隧道口上，口没了路由即失效；
	// 插件壳是配置投影，保留即可（用户 intent 不动）。
}
