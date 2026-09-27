package main

// 托盘状态机 —— 纯逻辑，不碰网络/进程/托盘，便于单测。
//
// 为什么要自己的状态机而不是直接渲染 /api/v1/node-status：
//   · mynetd **停着的时候**该接口根本不存在，托盘必须自己判「Stopped」
//   · 启停是**过渡态**（Starting/Stopping），node-status 只有稳态
//   · 托盘菜单的按钮可用性必须由同一张表给出，不能 UI 各判各的（会漂）
//
// 与 node-status（mynetd/localapi/node_status.go）的对应：
//
//	托盘          node-status
//	─────────     ──────────
//	Stopped       （无响应）
//	Starting      （过渡）
//	Running       RUNNING
//	Degraded      DEGRADED
//	Failed        FAILED / 启停超时
//	Stopping      （过渡）
//
// 判定原则（沿用 AGENTS.md §2.4）：转移表显式穷尽，
// 未定义事件一律 no-op 并返回 false，**不猜**。

// State 托盘/节点生命周期状态。
type State int

const (
	// StateStopped 守护进程不在跑（API 无响应）。
	StateStopped State = iota
	// StateStarting 已发启动，等待 API 起来。
	StateStarting
	// StateRunning API 起来了，node-status 报 RUNNING。
	StateRunning
	// StateDegraded API 起来了，node-status 报 DEGRADED（缺路由/对端不通）。
	StateDegraded
	// StateFailed 启动失败、运行中崩掉未恢复，或 node-status 报 FAILED。
	StateFailed
	// StateStopping 已发停止，等待 API 消失。
	StateStopping
)

// String 稳定的英文名（日志/测试断言用；UI 文案另走 Label）。
func (s State) String() string {
	switch s {
	case StateStopped:
		return "Stopped"
	case StateStarting:
		return "Starting"
	case StateRunning:
		return "Running"
	case StateDegraded:
		return "Degraded"
	case StateFailed:
		return "Failed"
	case StateStopping:
		return "Stopping"
	default:
		return "Unknown"
	}
}

// Label 中文一言之（托盘 tooltip / 菜单首行）。
func (s State) Label() string {
	switch s {
	case StateStopped:
		return "已停止"
	case StateStarting:
		return "启动中…"
	case StateRunning:
		return "运行中"
	case StateDegraded:
		return "降级运行"
	case StateFailed:
		return "异常"
	case StateStopping:
		return "停止中…"
	default:
		return "未知"
	}
}

// Event 驱动状态转移的事件。
type Event int

const (
	// EvStart 用户点了「启动」。
	EvStart Event = iota
	// EvStop 用户点了「停止」。
	EvStop
	// EvDaemonUp 本机 API 有响应。
	EvDaemonUp
	// EvDaemonDown 本机 API 无响应。
	EvDaemonDown
	// EvNodeHealthy node-status = RUNNING。
	EvNodeHealthy
	// EvNodeDegraded node-status = DEGRADED。
	EvNodeDegraded
	// EvNodeFailed node-status = FAILED。
	EvNodeFailed
	// EvStartTimeout 启动等待超时。
	EvStartTimeout
	// EvStopTimeout 停止等待超时。
	EvStopTimeout
)

func (e Event) String() string {
	switch e {
	case EvStart:
		return "Start"
	case EvStop:
		return "Stop"
	case EvDaemonUp:
		return "DaemonUp"
	case EvDaemonDown:
		return "DaemonDown"
	case EvNodeHealthy:
		return "NodeHealthy"
	case EvNodeDegraded:
		return "NodeDegraded"
	case EvNodeFailed:
		return "NodeFailed"
	case EvStartTimeout:
		return "StartTimeout"
	case EvStopTimeout:
		return "StopTimeout"
	default:
		return "Unknown"
	}
}

// Action 状态机顺带发出的「该去做的事」。空 Action 表示只换状态。
type Action int

const (
	ActionNone Action = iota
	// ActionDoStart 真的去拉起 mynetd。
	ActionDoStart
	// ActionDoStop 真的去停 mynetd。
	ActionDoStop
	// ActionReportError 需要弹错（启动失败/超时）。
	ActionReportError
)

func (a Action) String() string {
	switch a {
	case ActionNone:
		return "None"
	case ActionDoStart:
		return "DoStart"
	case ActionDoStop:
		return "DoStop"
	case ActionReportError:
		return "ReportError"
	default:
		return "Unknown"
	}
}

// FSM 托盘状态机。非并发安全 —— 调用方串行化（托盘事件循环本来就单线程）。
type FSM struct {
	state State
	// lastErr 最近一次失败的人话说明（启动超时等），UI 展示用。
	lastErr string
}

// NewFSM 初始为 Stopped（装完托盘、daemon 未跑的常态）。
func NewFSM() *FSM { return &FSM{state: StateStopped} }

// State 当前状态。
func (f *FSM) State() State { return f.state }

// LastError 最近失败说明；无则空串。
func (f *FSM) LastError() string { return f.lastErr }

// On 投递事件。返回 (新状态, 要执行的动作, 是否发生了转移)。
// 未定义转移返回 (原状态, ActionNone, false) —— 调用方**不要**据此报错。
func (f *FSM) On(ev Event) (State, Action, bool) {
	prev := f.state
	act := ActionNone

	// 显式转移表。每个分支都必须写全，漏写 = no-op（可测出）。
	switch f.state {
	case StateStopped:
		switch ev {
		case EvStart:
			f.state = StateStarting
			act = ActionDoStart
		case EvDaemonUp, EvNodeHealthy:
			// 托盘冷启动时 daemon 可能已经在跑且健康
			f.state = StateRunning
		case EvNodeDegraded:
			f.state = StateDegraded
		case EvNodeFailed:
			f.state = StateFailed
			f.lastErr = "节点状态上报 FAILED"
		}

	case StateStarting:
		switch ev {
		case EvDaemonUp:
			f.state = StateRunning
		case EvStartTimeout:
			f.state = StateFailed
			f.lastErr = "启动超时：本机 API 一直没起来"
			act = ActionReportError
		case EvStop:
			f.state = StateStopping
			act = ActionDoStop
		case EvDaemonDown:
			// 启动中暂时无响应是正常的，不转移
		}

	case StateRunning:
		switch ev {
		case EvStop:
			f.state = StateStopping
			act = ActionDoStop
		case EvDaemonDown:
			// 崩了、被外部停了、或用户先停了节点 —— 都是「已停止」，不是异常
			f.state = StateStopped
			f.lastErr = ""
		case EvNodeDegraded:
			f.state = StateDegraded
		case EvNodeFailed:
			f.state = StateFailed
			f.lastErr = "节点状态上报 FAILED"
		case EvDaemonUp, EvNodeHealthy:
			// 已经是 Running，探针回声
		}

	case StateDegraded:
		switch ev {
		case EvNodeHealthy:
			f.state = StateRunning
		case EvNodeFailed:
			f.state = StateFailed
			f.lastErr = "节点状态上报 FAILED"
		case EvStop:
			f.state = StateStopping
			act = ActionDoStop
		case EvDaemonDown:
			f.state = StateStopped
			f.lastErr = ""
		}

	case StateFailed:
		switch ev {
		case EvStart:
			// 重试
			f.state = StateStarting
			f.lastErr = ""
			act = ActionDoStart
		case EvStop:
			f.state = StateStopping
			act = ActionDoStop
		case EvDaemonDown:
			// 失败后进程没了 = 已停止，清错误，不再顶着「异常」
			f.state = StateStopped
			f.lastErr = ""
		case EvDaemonUp:
			// 服务其实起来了但之前判失败 —— 重新按健康态走
			f.state = StateRunning
			f.lastErr = ""
		}

	case StateStopping:
		switch ev {
		case EvDaemonDown:
			f.state = StateStopped
			f.lastErr = ""
		case EvStopTimeout:
			f.state = StateFailed
			f.lastErr = "停止超时：进程似乎还在"
			act = ActionReportError
		case EvStop:
			// 幂等
		case EvDaemonUp:
			// 停的过程中探针又通了，可能没停干净；保持 Stopping 等下一个探测
		}
	}

	ok := f.state != prev
	return f.state, act, ok
}

// CanStart 菜单「启动」是否可点。
func (f *FSM) CanStart() bool {
	return f.state == StateStopped || f.state == StateFailed
}

// CanStop 菜单「停止」是否可点。
func (f *FSM) CanStop() bool {
	return f.state == StateRunning || f.state == StateDegraded || f.state == StateFailed || f.state == StateStarting
}

// Busy 是否处于过渡态（菜单应禁用启停，显示「…」）。
func (f *FSM) Busy() bool {
	return f.state == StateStarting || f.state == StateStopping
}
