package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/mynet-club/mynet_agent-V2/mynetd/commands"
	"github.com/mynet-club/mynet_agent-V2/shared/logger"
)

// commandPullTimeout 一次 command_queued 处理（拉取 + 执行 + 回报）的总超时。
const commandPullTimeout = 2 * time.Minute

// 网关事件 → 执行器动作的映射。
//
// back 的 GatewayNotifier 发三类事件，且 data 仅带定位信息（command_id / generation）：
//
//	route_changed  → mynet refresh-routes --auto（拓扑/路由变化，就地可完成）
//	bundle_changed → mynet refresh-nodes --auto（配置包 generation 变更，就地可完成）
//	command_queued → **pull 模型**：回 back 拉 GET /api/v1/monitor/commands，
//	                 逐条执行并回报（见 runCommandPull）
//
// 前两类映射到既有的 commands.Plan argv，避免重抄一份命令表。
var eventToOp = map[string]string{
	"bundle_changed": commands.OpNodeRefresh,
	"route_changed":  commands.OpRouteRefresh,
}

// eventDispatcher 串行执行事件触发的动作。
//
// 为什么要有 inFlight 守卫：事件处理跑在独立 goroutine 上（不能阻塞 WS 读循环，
// 否则 ping 与结果回传都卡住），但若 back 连续广播，多个 `mynet apply` /
// `refresh-*` 会并发写 OS 路由与驱动配置——这正是本项目踩过的坑
// （跨驱动路由互相污染）。同一时刻只允许一个在跑，后来的直接丢弃并记日志：
// 事件语义是「有变化，去刷新」，不是「刷新 N 次」，合并是安全的。
type eventDispatcher struct {
	exec     func(argv []string) (string, string, error)
	nodeID   string
	inFlight atomic.Bool
	logf     func(format string, args ...any)

	// pull / report 构成 command_queued 的完整链路。
	// 二者可为 nil（未配 gateway 或无节点身份），此时 command_queued 明确记日志说明缺什么，
	// 而不是静默忽略——静默忽略的症状是「back 下了命令但节点毫无反应」。
	pull   func(ctx context.Context) ([]pendingCommand, error)
	report func(ctx context.Context, res commands.Result) error
}

// maxCommandsPerEvent 单次 command_queued 最多处理多少条命令。
//
// back 每次只返 1 条并标记为执行中，正常情况拉几轮就空了。设上限是防止
// back 侧状态机异常（命令永远回到 pending）时把节点拖进死循环。
const maxCommandsPerEvent = 32

func (d *eventDispatcher) Handle(name string, data map[string]string) {
	if name == "command_queued" {
		d.runCommandPull(data["command_id"])
		return
	}

	op, ok := eventToOp[name]
	if !ok {
		d.logf("event: 未知事件 %q，忽略", name)
		return
	}
	d.runExclusive(name, func() {
		res := commands.Run(d.exec, commands.Request{Op: op}, d.nodeID)
		if res.OK {
			d.logf("event: %s → %s 执行成功", name, op)
			return
		}
		d.logf("event: %s → %s 执行失败: %s%s", name, op, res.Error, stderrSuffix(res.Stderr))
	})
}

// runCommandPull 收到 command_queued 后回 back 拉命令、执行、回报结果。
//
// 这是 pull 模型：back 的 GatewayNotifier 只发「有新命令待执行」的通知，
// 命令内容要节点自己取（`GET /api/v1/monitor/commands`，back 侧 docblock 明写
// 「供控制面长连接在收到 command_queued 事件后调用」）。
func (d *eventDispatcher) runCommandPull(commandID string) {
	if d.pull == nil || d.report == nil {
		d.logf("event: command_queued（command_id=%s）—— 命令拉取链未装配"+
			"（缺 gateway 地址或节点身份），节点无法执行服务端下发的命令", commandID)
		return
	}

	d.runExclusive("command_queued", func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandPullTimeout)
		defer cancel()

		for i := 0; i < maxCommandsPerEvent; i++ {
			batch, err := d.pull(ctx)
			if err != nil {
				d.logf("event: 拉取待执行命令失败: %v", err)
				return
			}
			if len(batch) == 0 {
				if i == 0 {
					d.logf("event: command_queued（command_id=%s）但 back 无待执行命令"+
						"（可能已被别的连接取走或已过期）", commandID)
				}
				return
			}

			for _, cmd := range batch {
				d.executeOne(ctx, cmd)
			}
		}
		d.logf("event: 单次 command_queued 处理已达上限 %d 条，剩余留待下次事件或心跳轮询",
			maxCommandsPerEvent)
	})
}

// executeOne 执行一条命令并回报结果。
//
// 映射不了 op 时**照样回报失败**，不能静默丢弃：back 侧那条命令已被标记为
// 「执行中」，不回报就会永远卡在那个状态（CPI-4 的教训：静默失效最难查）。
func (d *eventDispatcher) executeOne(ctx context.Context, cmd pendingCommand) {
	id := cmd.ID.String()

	op, ok := commands.OpFromBackCommand(cmd.Command)
	if !ok {
		d.logf("command: back 下发了 agent 不认识的命令 %q（id=%s）—— 按失败回报，不静默忽略",
			cmd.Command, id)
		d.reportResult(ctx, commands.Result{
			ID:    id,
			OK:    false,
			Error: "unknown command: " + cmd.Command,
		})
		return
	}

	req := commands.Request{ID: id, Op: op, Args: stringifyParams(cmd.Params)}
	d.logf("command: 执行 %s → op=%s (id=%s)", cmd.Command, op, id)
	res := commands.Run(d.exec, req, d.nodeID)
	if !res.OK {
		d.logf("command: %s 执行失败: %s%s", cmd.Command, res.Error, stderrSuffix(res.Stderr))
	}
	d.reportResult(ctx, res)
}

func (d *eventDispatcher) reportResult(ctx context.Context, res commands.Result) {
	if err := d.report(ctx, res); err != nil {
		// 回报失败必须吵出来：命令执行了但 back 不知道，会一直显示「执行中」。
		d.logf("command: 结果回报失败（id=%s）: %v", res.ID, err)
	}
}

// runExclusive 在单飞守卫下跑一个动作。
func (d *eventDispatcher) runExclusive(name string, fn func()) {
	if !d.inFlight.CompareAndSwap(false, true) {
		d.logf("event: %s 已有动作在跑，合并本次（事件语义是「去处理」而非「处理 N 次」）", name)
		return
	}
	go func() {
		defer d.inFlight.Store(false)
		fn()
	}()
}

// stringifyParams 把 back 的 params（任意 JSON 值）压成 commands.Request 要的
// map[string]string。非字符串值用 JSON 表示，避免丢信息。
func stringifyParams(params map[string]any) map[string]string {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]string, len(params))
	for k, v := range params {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
			out[k] = ""
		default:
			if raw, err := json.Marshal(t); err == nil {
				out[k] = string(raw)
			} else {
				out[k] = fmt.Sprintf("%v", t)
			}
		}
	}
	return out
}

func stderrSuffix(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > 200 {
		s = s[len(s)-200:]
	}
	return " | stderr: " + s
}

// newEventDispatcher 组装事件分派器。
//
// exec 复用 mynetd 的执行器（桌面 exec mynet）。pull/report 为 nil 时
// command_queued 会明确记日志说明缺什么（见 runCommandPull）。
//
// 事件处理不持有长生命周期 ctx：动作由 runCommandPull 内部按 commandPullTimeout
// 限时，执行器自身也带超时（execMynet 内 60s）。
func newEventDispatcher(
	nodeID string,
	exec func(argv []string) (string, string, error),
	pull func(ctx context.Context) ([]pendingCommand, error),
	report func(ctx context.Context, res commands.Result) error,
) *eventDispatcher {
	return &eventDispatcher{
		exec:   exec,
		nodeID: nodeID,
		logf:   func(f string, a ...any) { logger.Info(fmt.Sprintf(f, a...)) },
		pull:   pull,
		report: report,
	}
}
