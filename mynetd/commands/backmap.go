package commands

import "strings"

// back 下发的 command 名 → agent 的 Op。
//
// ── 为什么需要这层翻译 ──────────────────────────────────────────────
// back 的 `monitor_rt_cmds.command` 用的是 `mynet.<op>` 形式，agent 的 Plan()
// 认的是去掉前缀的 `<op>`。实测 13 条里 9 条严格吻合（见 TestBackCommandVocabulary），
// 剩下 4 条是 CPI-4 时代漂掉的残留（mynet.start / stop / restart / service.restart）。
//
// ⚠️ 不认识的命令**必须显式报错**，不能静默忽略：历史上 back 单方面声明的
// `mynet.reboot` 就是因为节点侧静默不认，让「重启节点」整整失效了一段时间，
// 而且**只在节点本地日志里留一行**，从 back 侧完全看不出来。
const backCommandPrefix = "mynet."

// legacyCommandAliases 4 条历史漂移命令的显式映射。
//
// 放在 agent 侧（命令名的唯一真值来源）而不是改 back，是为了让 back 已入库、
// 已排队的旧命令仍能执行——否则升级 agent 后那些命令会集体变成「不识别」。
var legacyCommandAliases = map[string]string{
	"start":           OpServiceStart,
	"stop":            OpServiceStop,
	"restart":         OpServiceReload,
	"service.restart": OpServiceReload,
}

// OpFromBackCommand 把 back 的 command 名翻译成 agent 的 Op。
//
// **严格要求 `mynet.` 前缀**：back 实测下发的 13 条命令全部带前缀，
// 裸 op（如 `node.update`）一律拒。这是系统边界，宁可严格——
// 若哪天 back 的命名约定变了，这里会立刻显式失败，而不是半通不通地静默工作。
//
// ok=false 表示 back 下发了 agent 不认识的命令。调用方必须把它当成**错误**上报，
// 而不是当作「无事发生」。
func OpFromBackCommand(command string) (op string, ok bool) {
	c := strings.TrimSpace(command)
	if !strings.HasPrefix(c, backCommandPrefix) {
		return "", false
	}

	body := strings.TrimPrefix(c, backCommandPrefix)
	if body == "" {
		return "", false
	}

	// 先查历史别名，再按「去前缀即 op」的常规规则。
	if alias, found := legacyCommandAliases[body]; found {
		return alias, true
	}
	if IsKnownOp(body) {
		return body, true
	}
	return "", false
}

// knownOps 是 Plan() 实际能翻译的全部 Op。
//
// 单独列一份而不是从 Plan 的 switch 反射，是为了让词汇守护测试有一个
// 可枚举的真值集合——Plan 的 switch 无法被遍历。
// ⚠️ 改 Plan 的 case 必须同步改这里，TestKnownOpsCoveredByPlan 会拦。
var knownOps = []string{
	OpRefreshRoutesGnb,
	OpRefreshAddressGnb,
	OpRefreshRoutesWG,
	OpRefreshAddressWG,
	OpRouteRefresh,
	OpRouteUpdate,
	OpNodeRefresh,
	OpNodeUpdate,
	OpDetector,
	OpMetrics,
	OpServiceStart,
	OpServiceStop,
	OpServiceReload,
}

// IsKnownOp 报告 op 是否能被 Plan() 翻译。
func IsKnownOp(op string) bool {
	for _, k := range knownOps {
		if k == op {
			return true
		}
	}
	return false
}

// KnownOps 返回 Plan() 支持的全部 Op（副本）。
func KnownOps() []string {
	out := make([]string, len(knownOps))
	copy(out, knownOps)
	return out
}

// BackCommands 返回 agent 能接受的、back 侧完整 command 名集合。
// 供词汇守护测试与文档生成使用。
func BackCommands() []string {
	out := make([]string, 0, len(knownOps)+len(legacyCommandAliases))
	for _, op := range knownOps {
		out = append(out, backCommandPrefix+op)
	}
	for legacy := range legacyCommandAliases {
		out = append(out, backCommandPrefix+legacy)
	}
	return out
}
