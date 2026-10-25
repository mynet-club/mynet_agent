package mynet

// 隧道启停联动：GW 的 DNS 由 apply 写网关产物，PC 的系统 DNS 只由 hook 接管。
// up: tunnel → apply(proxy / gateway DNS) → proxy projection → PC dns-hook
// down: dns-hook → owned DNS restore → tunnel；用户加速偏好保留。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// ── 加速意图（用户偏好，与进程事实分离）────────────────────────

// dnsHookIntentFile 用户「加速」意图（托盘/面板开关写入，隧道重启时读取）。
// 与运行态 dns-hook.pid 分开：pid 是进程事实，这里是用户偏好。
func dnsHookIntentFile() string {
	return filepath.Join(varDir(), "proxy", "dns-hook-intent.json")
}

type dnsHookIntent struct {
	Enabled bool `json:"enabled"`
	Version int  `json:"version,omitempty"`
}

// SetDNSHookWanted 记住加速偏好（托盘/CLI 用户动作）。
func SetDNSHookWanted(on bool) error {
	// PC 加速与客户端 proxy 共用一个持久开关；旧文件仅保留兼容。
	if err := SetProxyEnabled(on); err != nil {
		return err
	}
	return saveDNSHookIntent(on)
}

func saveDNSHookIntent(on bool) error {
	b, _ := json.MarshalIndent(dnsHookIntent{Enabled: on, Version: 1}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(dnsHookIntentFile()), 0o755); err != nil {
		return err
	}
	path := dnsHookIntentFile()
	if err := os.WriteFile(path+".tmp", append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// legacyDNSHookIntent 仅未收口的旧文件有独立开关语义。
func legacyDNSHookIntent() *dnsHookIntent {
	raw, err := os.ReadFile(dnsHookIntentFile())
	var it dnsHookIntent
	if err != nil || json.Unmarshal(raw, &it) != nil || it.Version != 0 {
		return nil
	}
	return &it
}

// migratePCAcceleration 保留旧版最后一次加速选择，在隧道投影前归入 proxy 策略。
func migratePCAcceleration() error {
	if !isPCDNSPlatform() {
		return nil
	}
	if it := legacyDNSHookIntent(); it != nil {
		return SetDNSHookWanted(it.Enabled)
	}
	return nil
}

// DNSHookWanted 统一读取客户端 proxy 开关；旧版独立意图在迁移前保留优先级。
func DNSHookWanted() bool {
	if it := legacyDNSHookIntent(); it != nil {
		return it.Enabled
	}
	st, err := LoadProxyState()
	if err != nil {
		return false
	}
	if st.Local != nil {
		return st.Local.Policy.Enabled && st.Local.Policy.Role != domain.ProxyRoleServer
	}
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
	if err := SyncPluginProxyFromPolicy(p); err != nil {
		return sidecarResult{name: "proxy", err: err}
	}
	if err := ApplyPlugins(false); err != nil {
		return sidecarResult{name: "proxy", err: err}
	}
	return sidecarResult{name: "proxy"}
}

// startDNSHookSidecar 加速钩子（叠加层）：意图开着且隧道已在，才拉起。
// 依赖：tunnel（钉扎目标）+ DomainRules（清单，随 EffectiveProxyPolicy 读）。
func startDNSHookSidecar() sidecarResult {
	if !isPCDNSPlatform() || !DNSHookWanted() {
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
	if !dnsHookRunning() && !hookWatcherAlive() && !DNSRestoreNeeded() && len(LoadDNSHookPins()) == 0 {
		return sidecarResult{name: "dns-hook", err: nil}
	}
	if rc := dnsHookStop(); rc != 0 {
		return sidecarResult{name: "dns-hook", err: fmt.Errorf("exit %d", rc)}
	}
	return sidecarResult{name: "dns-hook"}
}

// restoreDNSSidecar 幂等还原系统 DNS（历史接管 / 三选一落地过的都收）。
func restoreDNSSidecar() sidecarResult {
	if !DNSRestoreNeeded() {
		return sidecarResult{name: "dns-restore"}
	}
	if err := RestoreSystemDNS(); err != nil {
		return sidecarResult{name: "dns-restore", err: err}
	}
	return sidecarResult{name: "dns-restore"}
}

// SyncSidecarsOnTunnelUp 隧道起来后的侧车编排。
//
//	第 1 层：proxy 投影（GW DNS 已由 apply 落地）
//	第 2 层（叠加）：dns-hook
//
// 单项失败只打 ⚠，不回滚隧道 —— 侧车是增强，不是隧道前提。
func SyncSidecarsOnTunnelUp() {
	// 不再从旧 peer/smart 配置二次接管系统 DNS；PC 只认 hook 意图。
	syncProxySidecar().log()
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
