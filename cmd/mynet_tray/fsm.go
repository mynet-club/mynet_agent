package main

// 托盘状态机 —— 纯逻辑，不碰网络/进程/托盘，便于单测。
//
// 为什么要自己的状态机而不是直接渲染 /api/v1/node-status：
//   · mynetd **停着的时候**该接口根本不存在，托盘必须自己判「服务不可用」
//   · 启停是**过渡态**（Starting/Stopping），node-status 只有稳态
//   · 托盘菜单的按钮可用性必须由同一张表给出，不能 UI 各判各的（会漂）
//
// 与 node-status（mynetd/localapi/node_status.go）的对应：
//
//	托盘          node-status
//	─────────     ──────────
//	SvcDown       （无响应 / 非200 / 信封不可信）
//	Stopped       STOPPED / UNINSTALLED / INSTALLED / CONFIGURED
//	Starting      （过渡）
//	Running       RUNNING
//	Degraded      DEGRADED
//	Failed        FAILED / 启停超时
//	Stopping      （过渡）
//
// ⚠️ SvcDown 与 Stopped 必须分开（D3）：前者用户该去查服务，后者用户该点连接。
// 把两者混成一个「已停止」会让「mynetd 没装/没起」显示成「隧道没连」，
// 用户点连接只会失败，且看不到该做什么。
//
// 判定原则（沿用 AGENTS.md §2.4）：转移表显式穷尽，
// 未定义事件一律 no-op 并返回 false，**不猜**。
//
// ⚠️ 转移表的每个事件都必须能被 eventFromProbe 真实产生（D1/D2 的教训）：
// 旧版有一条 EvDaemonUp 边，而探针从不产生它，于是 Starting 只能靠超时退出、
// Failed 成了终态 —— 测试手工喂 EvDaemonUp 全绿，产品上连接永远「启动超时」。

// State 托盘/节点生命周期状态。
type State int

const (
	// StateStopped 节点未连接：本机服务在，隧道没起。用户可以点连接。
	StateStopped State = iota
	// StateStarting 已发连接，等待节点事实变健康。
	StateStarting
	// StateRunning 节点 RUNNING 且进程在。
	StateRunning
	// StateDegraded 节点 DEGRADED（缺路由/对端不通）。
	StateDegraded
	// StateFailed 连接失败、运行中崩掉未恢复，或 node-status 报 FAILED。
	StateFailed
	// StateStopping 已发停止，等待节点事实变未连接。
	StateStopping
	// StateSvcDown 本机服务不可用（API 无响应/非200），或有响应但状态不可信。
	// 此时启停都做不了，按钮一律禁用。
	StateSvcDown
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
	case StateSvcDown:
		return "SvcDown"
	default:
		return "Unknown"
	}
}

// Label 中文一言之（托盘 tooltip / 菜单首行）。
func (s State) Label() string {
	switch s {
	case StateStopped:
		return "未连接"
	case StateStarting:
		return "正在连接…"
	case StateRunning:
		return "已连接"
	case StateDegraded:
		return "连接受限"
	case StateFailed:
		return "连接异常"
	case StateStopping:
		return "正在断开…"
	case StateSvcDown:
		return "本机服务不可用"
	default:
		return "状态暂不可用"
	}
}

// Event 驱动状态转移的事件。
//
// 每个事件都必须有真实产生者：EvStart/EvStop 来自菜单点击，
// 其余全部来自 eventFromProbe（status.go）或超时判定（tray.go refreshOnce）。
type Event int

const (
	// EvStart 用户点了「连接」。
	EvStart Event = iota
	// EvStop 用户点了「断开」。
	EvStop
	// EvDaemonDown 服务在、状态可信，但节点没在跑（未连接）。
	EvDaemonDown
	// EvNodeHealthy node-status = RUNNING 且 process_up。
	EvNodeHealthy
	// EvNodeDegraded node-status = DEGRADED 且 process_up。
	EvNodeDegraded
	// EvNodeFailed node-status = FAILED 且 process_up。
	EvNodeFailed
	// EvStartFailed 连接命令本身失败（打不到 API / 业务拒绝 / 未登录）。
	// 与超时是两件事：失败有具体原因，超时只有「等不到」。
	// lastErr 由调用方先设好（必须是安全文案），本事件不覆盖它。
	EvStartFailed
	// EvStartTimeout 连接等待超时：命令返回成功了，但探针一直不报健康。
	EvStartTimeout
	// EvStopFailed 断开命令本身失败。同样不覆盖调用方设的 lastErr。
	EvStopFailed
	// EvStopTimeout 断开等待超时。
	EvStopTimeout
	// EvSvcDown 本机 API 无响应（D3：不等于「未连接」）。
	EvSvcDown
	// EvSvcInvalid 本机 API 有响应但状态不可信：非200、信封非法、缺 data
	// （D4：200 不等于健康，未知 schema 不等于未连接）。
	EvSvcInvalid
)

func (e Event) String() string {
	switch e {
	case EvStart:
		return "Start"
	case EvStop:
		return "Stop"
	case EvDaemonDown:
		return "NodeDown"
	case EvNodeHealthy:
		return "NodeHealthy"
	case EvNodeDegraded:
		return "NodeDegraded"
	case EvNodeFailed:
		return "NodeFailed"
	case EvStartFailed:
		return "StartFailed"
	case EvStartTimeout:
		return "StartTimeout"
	case EvStopFailed:
		return "StopFailed"
	case EvStopTimeout:
		return "StopTimeout"
	case EvSvcDown:
		return "SvcDown"
	case EvSvcInvalid:
		return "SvcInvalid"
	default:
		return "Unknown"
	}
}

// Action 状态机顺带发出的「该去做的事」。空 Action 表示只换状态。
type Action int

const (
	ActionNone Action = iota
	// ActionDoStart 真的去连（POST /api/v1/config/connect）。
	ActionDoStart
	// ActionDoStop 真的去断开（service stop）。
	ActionDoStop
	// ActionReportError 需要弹错（连接失败/超时）。
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

// 服务面失败的人话说明。FSM 自己写，不让调用方拼原文（D7/D20）。
const (
	msgSvcDown      = "本机服务未响应，请确认 MyNet 已安装并在运行"
	msgSvcInvalid   = "本机状态响应不可识别，请刷新或打开面板查看"
	msgNodeFailed   = "节点状态上报 FAILED"
	msgStartTimeout = "连接超时：本机服务一直没报连接成功"
	msgStopTimeout  = "断开超时：隧道似乎还在"
)

// FSM 托盘状态机。非并发安全 —— 调用方串行化（P2 负责加同步）。
type FSM struct {
	state State
	// lastErr 最近一次失败的人话说明，UI 展示用。
	// ⚠️ 只放安全文案：调用方设进来的字符串必须已过 safeText。
	lastErr string
}

// NewFSM 初始为 SvcDown —— 冷启动还没探过，不能先闪「未连接」（那等于
// 断言服务在跑而节点没连，是未经验证的结论）。
func NewFSM() *FSM { return &FSM{state: StateSvcDown} }

// State 当前状态。
func (f *FSM) State() State { return f.state }

// LastError 最近失败说明；无则空串。
func (f *FSM) LastError() string { return f.lastErr }

// On 投递事件。返回 (新状态, 要执行的动作, 是否发生了转移)。
// 未定义转移返回 (原状态, ActionNone, false) —— 调用方**不要**据此报错。
func (f *FSM) On(ev Event) (State, Action, bool) {
	prev := f.state
	act := ActionNone

	// 服务面失败优先于节点事实：探针连不上/不可信时，任何节点态都不成立。
	if ev == EvSvcDown || ev == EvSvcInvalid {
		f.state = StateSvcDown
		if ev == EvSvcDown {
			f.lastErr = msgSvcDown
		} else {
			f.lastErr = msgSvcInvalid
		}
		return f.state, ActionNone, f.state != prev
	}

	// 显式转移表。每个分支都必须写全，漏写 = no-op（可测出）。
	switch f.state {
	case StateStopped:
		switch ev {
		case EvStart:
			f.state = StateStarting
			f.lastErr = ""
			act = ActionDoStart
		case EvNodeHealthy:
			// 托盘冷启动时隧道可能已经在跑且健康
			f.state = StateRunning
		case EvNodeDegraded:
			f.state = StateDegraded
		case EvNodeFailed:
			f.state = StateFailed
			f.lastErr = msgNodeFailed
		case EvDaemonDown:
			// 已经是未连接，探针回声
		}

	case StateStarting:
		switch ev {
		// D1：连接完成必须由**真实探针事实**结束过渡，不能只等 EvDaemonUp。
		case EvNodeHealthy:
			f.state = StateRunning
			f.lastErr = ""
		case EvNodeDegraded:
			f.state = StateDegraded
			f.lastErr = ""
		case EvNodeFailed:
			f.state = StateFailed
			f.lastErr = msgNodeFailed
		case EvStartFailed:
			// 原因由调用方先写进 lastErr（已是安全文案）；这里只落状态，
			// 不覆盖成通用超时文案（§3「原始错误被超时文案覆盖」）。
			f.state = StateFailed
			act = ActionReportError
		case EvStartTimeout:
			f.state = StateFailed
			if f.lastErr == "" {
				f.lastErr = msgStartTimeout
			}
			act = ActionReportError
		case EvStop:
			f.state = StateStopping
			act = ActionDoStop
		case EvDaemonDown:
			// 连接中节点还没起来是正常的，不转移（等超时或后续事实）
		}

	case StateRunning:
		switch ev {
		case EvStop:
			f.state = StateStopping
			act = ActionDoStop
		case EvDaemonDown:
			// 崩了、被外部停了、或用户先停了节点 —— 都是「未连接」，不是异常
			f.state = StateStopped
			f.lastErr = ""
		case EvNodeDegraded:
			f.state = StateDegraded
		case EvNodeFailed:
			f.state = StateFailed
			f.lastErr = msgNodeFailed
		case EvNodeHealthy:
			// 已经是 Running，探针回声
		}

	case StateDegraded:
		switch ev {
		case EvNodeHealthy:
			f.state = StateRunning
			f.lastErr = ""
		case EvNodeFailed:
			f.state = StateFailed
			f.lastErr = msgNodeFailed
		case EvStop:
			f.state = StateStopping
			act = ActionDoStop
		case EvDaemonDown:
			f.state = StateStopped
			f.lastErr = ""
		}

	case StateFailed:
		switch ev {
		// D2：Failed 不能是终态 —— 节点自己恢复了就该如实反映，
		// 不该逼用户重启托盘。
		case EvNodeHealthy:
			f.state = StateRunning
			f.lastErr = ""
		case EvNodeDegraded:
			f.state = StateDegraded
			f.lastErr = ""
		case EvStart:
			// 重试
			f.state = StateStarting
			f.lastErr = ""
			act = ActionDoStart
		case EvStop:
			f.state = StateStopping
			act = ActionDoStop
		case EvDaemonDown:
			// 失败后进程没了 = 未连接，清错误，不再顶着「连接异常」
			f.state = StateStopped
			f.lastErr = ""
		}

	case StateStopping:
		switch ev {
		case EvDaemonDown:
			f.state = StateStopped
			f.lastErr = ""
		case EvStopFailed:
			// 同 EvStartFailed：原因由调用方先写，这里只落状态。
			f.state = StateFailed
			act = ActionReportError
		case EvStopTimeout:
			f.state = StateFailed
			if f.lastErr == "" {
				f.lastErr = msgStopTimeout
			}
			act = ActionReportError
		case EvStop:
			// 幂等
		case EvNodeHealthy, EvNodeDegraded:
			// 停的过程中节点又健康了，可能没停干净；保持 Stopping 等下一个探测
		}

	case StateSvcDown:
		switch ev {
		// 服务恢复后按真实节点事实落位，不需要用户重启托盘。
		case EvNodeHealthy:
			f.state = StateRunning
			f.lastErr = ""
		case EvNodeDegraded:
			f.state = StateDegraded
			f.lastErr = ""
		case EvNodeFailed:
			f.state = StateFailed
			f.lastErr = msgNodeFailed
		case EvDaemonDown:
			f.state = StateStopped
			f.lastErr = ""
		case EvStart, EvStop:
			// 服务都连不上，启停必然失败；不做转移也不发动作
		}
	}

	ok := f.state != prev
	return f.state, act, ok
}

// CanStart 菜单「连接」是否可点。SvcDown 时不可点（点了也打不到 API）。
func (f *FSM) CanStart() bool {
	return f.state == StateStopped || f.state == StateFailed
}

// CanStop 菜单「断开」是否可点。
func (f *FSM) CanStop() bool {
	return f.state == StateRunning || f.state == StateDegraded || f.state == StateFailed || f.state == StateStarting
}

// Busy 是否处于过渡态（菜单应禁用启停，显示「…」）。
func (f *FSM) Busy() bool {
	return f.state == StateStarting || f.state == StateStopping
}
