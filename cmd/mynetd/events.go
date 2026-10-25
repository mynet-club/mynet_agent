package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mynet-club/mynet_agent/mynetd/commands"
	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// commandPullTimeout 一次 command_queued 处理（拉取 + 执行 + 回报）的总超时。
const commandPullTimeout = 2 * time.Minute

// 网关事件 → 执行器动作的映射。
//
// 后端的 GatewayNotifier 发三类事件，且 data 仅带定位信息（command_id / generation）：
//
//	route_changed  → mynet refresh-routes --auto（拓扑/路由变化，就地可完成）
//	bundle_changed → mynet refresh-config --auto（配置包 generation 变更，就地可完成）
//	command_queued → **pull 模型**：回后端拉 GET /api/v1/node/commands，
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
// 否则 ping 与结果回传都卡住），但若后端连续广播，多个 `mynet apply` /
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
	// 而不是静默忽略——静默忽略的症状是「后端下了命令但节点毫无反应」。
	pull   func(ctx context.Context) ([]pendingCommand, error)
	report func(ctx context.Context, res commands.Result) error

	// syncBundle 由 setBundleSyncer 注入（未装配时为 nil）。
	// bundle_changed 必须**先拉后刷**：refresh-config 读的是 var/bundle.json，
	// 不先拉就只能读到上一版、或者根本没有那个文件（V1-10① 的断点）。
	syncBundle func(ctx context.Context) (string, error)

	// tryConfigExclusive 与 HTTP 写面（connect / config 写）共用同一把互斥：
	// 拿到锁则 fn 独占执行，拿不到返回 false（用户写进行中，事件侧让路）。
	// 未装配时为 nil，退化为直接跑（旧单进程行为）。
	//
	// 为什么必须接：connect 的 `config pull` 自己会触发 bundle_changed，
	// 事件侧再跑 service.reload（= gnb Stop+Start）会与 connect 的 service start
	// 抢同一把驱动生命周期锁 —— 两边都卡到 exec 60s 被 SIGKILL，
	// 若恰在 Stop 之后被杀，隧道就停在「已断开」（2026-10-17 实测）。
	tryConfigExclusive func(fn func() error) bool

	// pending 是「跑的时候又来事件」的补跑队列（按事件名合并，见 runExclusive）。
	pendingMu sync.Mutex
	pending   map[string]func()

	// onTopologyChanged 拓扑落地后回调（组合根接到 heartbeatLoop.invalidateMetrics）。
	// 心跳的 metrics 快照默认只在启动时采一次；若不在这里失效，新加入的节点
	// 永远不会出现在心跳与平台对端列表里，直到进程重启。
	onTopologyChanged func()
}

// setOnTopologyChanged 注入拓扑落地回调（未装配时为空操作）。
func (d *eventDispatcher) setOnTopologyChanged(fn func()) {
	d.onTopologyChanged = fn
}

func (d *eventDispatcher) notifyTopologyChanged() {
	if d.onTopologyChanged != nil {
		d.onTopologyChanged()
	}
}

// setConfigExclusive 注入与 HTTP 写面共享的互斥（main.go 装配 api.TryRunExclusive）。
func (d *eventDispatcher) setConfigExclusive(fn func(func() error) bool) {
	d.tryConfigExclusive = fn
}

// bundleStatusUpdated 是 bundleStore.Sync 在拿到新版时返回的标记
// （另一种是 "unchanged"，即后端返 304）。
const bundleStatusUpdated = "updated"

// bundleSyncTimeout 一次 bundle 拉取的超时。
//
// 比 backRequestTimeout 短：它跑在事件处理路径上，卡住会让后续事件全排队
// （inFlight 守卫是串行的）。超时就放弃这一次 —— 事件侧的补偿不是靠心跳轮询
// 兜底，而是靠「下次 WS 连接建立时补拉」（transport.OnConnect），
// 断线窗口越长、重连越多次，追平的机会越多。
const bundleSyncTimeout = 15 * time.Second

// setBundleSyncer 注入 bundle 拉取能力。与 heartbeat 的 setReporter 同一套做法：
// 依赖后绑，好让「没配 gateway / 无节点身份」时是明确的 nil 而不是假实现。
func (d *eventDispatcher) setBundleSyncer(fn func(ctx context.Context) (string, error)) {
	d.syncBundle = fn
}

// pullBundle 拉一次 bundle。返回 (结果标记, 是否该继续跑刷新) ——
// false 表示**不该继续**：因为没有新配置可读，跑下去只会把「读 bundle 失败」
// 当成刷新失败报出来，那会把「上游没拉到」和「本地刷新坏了」两种故障混成一条日志。
func (d *eventDispatcher) pullBundle(name string) (string, bool) {
	if d.syncBundle == nil {
		d.logf("event: %s —— bundle 拉取链未装配（未配 MYNET_GATEWAYS 或节点面密钥未就绪），"+
			"只能沿用本地已有的 var/bundle.json", name)
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), bundleSyncTimeout)
	defer cancel()

	status, err := d.syncBundle(ctx)
	if err != nil {
		d.logf("event: %s —— bundle 拉取失败，跳过本次刷新: %s", name, err)
		return "", false
	}
	d.logf("event: %s —— bundle %s", name, status)
	return status, true
}

// catchUpBundle 连接建立时补一次拉取（由 transport.OnConnect 触发）。
//
// 这是 WS 下发的自愈路径：网关推送是 fire-and-forget，节点断开（网络抖动、网关
// 重启、背压判死）期间后端广播的 bundle_changed **不会补发**。以前说「心跳会
// 兜住」，但心跳只做采集上报、根本不拉 bundle —— 于是「错过一次推送」实际等于
// 「配置永久滞后，直到进程重启」。现在每次连上都对一次 ETag：
//   - 304（unchanged）→ 什么都不做，不重载驱动（避免无谓的隧道抖动）
//   - updated         → 走完整落地链 runBundleLifecycle
//   - 拉取失败        → 只记日志，下次重连再试
func (d *eventDispatcher) catchUpBundle() {
	status, ok := d.pullBundle("ws 连接建立")
	if !ok || status != bundleStatusUpdated {
		return
	}
	d.logf("event: 重连补拉到新配置（generation 变更），执行落地链")
	d.runExclusive("reconnect", d.runBundleLifecycle)
}

// maxCommandsPerEvent 单次 command_queued 最多处理多少条命令。
//
// 后端每次只返 1 条并标记为执行中，正常情况拉几轮就空了。设上限是防止
// 后端侧状态机异常（命令永远回到 pending）时把节点拖进死循环。
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
		// bundle_changed 先拉后刷（见 pullBundle）。route_changed 不需要 ——
		// 它刷的是拓扑/路由，就地可完成，不依赖后端下发的配置包。
		if name == "bundle_changed" {
			if _, ok := d.pullBundle(name); !ok {
				return
			}
			d.runBundleLifecycle()
			return
		}
		res := commands.Run(d.exec, commands.Request{Op: op}, d.nodeID)
		if res.OK {
			d.logf("event: %s → %s 执行成功", name, op)
			d.notifyTopologyChanged()
			return
		}
		d.logf("event: %s → %s 执行失败: %s%s", name, op, res.Error, stderrSuffix(res.Stderr))
	})
}

// runBundleLifecycle bundle 变更后的完整落地链。
//
// ⚠️ 此前 bundle_changed 只跑 refresh-config —— 也就是**只写配置文件**：
// 不重启驱动（gnb 的 HotReload 是 false，见 driver/gnb.go 的 Capabilities，
// 配置改了必须重启才生效），也不装内核路由。于是平台改了下发内容，
// 节点侧「收到了、写盘了、但完全没生效」，而这正是最难发现的一类故障
// （Phase 6 的 CPI-62 就是这个形态：隧道建好了、路由没装，ping 全丢）。
//
// 顺序**不能调换**：
//  1. refresh-config —— 写出驱动原生配置（node.conf/route.conf/address.conf/密钥）
//  2. service reload —— 重载驱动（Stop+Start），让它吃下新配置。
//     不能用 start：已在跑时会短路，而 gnb 的 HotReload 是 false。
//  3. apply --auto   —— 装内核路由。必须排在 2 之后：`ip route … dev gnb_tun`
//     要求接口已存在，否则直接失败
//
// 每步都单独报结果，不因为前一步成功就假定后面也成功。
func (d *eventDispatcher) runBundleLifecycle() {
	// refresh-config 始终跑：写驱动投影是本地幂等写盘，与用户写面无互斥必要。
	req := commands.Request{Op: commands.OpNodeRefresh}
	res := commands.Run(d.exec, req, d.nodeID)
	if res.OK {
		d.logf("event: bundle_changed → 刷新驱动配置（node.refresh）成功")
	} else {
		d.logf("event: bundle_changed → 刷新驱动配置（node.refresh）失败: %s%s",
			res.Error, stderrSuffix(res.Stderr))
	}

	// 指纹：与「上次成功 reload 时」比。refresh-config 已写完新投影，
	// 若内容与上次 reload 后一致，说明 bundle 只动了 keys/status 等元数据 ——
	// 再 Stop+Start 一次会把好端端的隧道打断（「通了不久就断」的一类）。
	fp := driverConfFingerprint()
	if fp != "" && fp == lastReloadFingerprint() && d.driverLikelyRunning() {
		d.logf("event: bundle_changed → 驱动配置未变，跳过 reload（保留当前隧道）")
		// 路由仍刷：bundle 可能只动了 OS 路由段。
		d.runLifecycleStep("装内核路由", commands.OpRouteUpdate)
		d.notifyTopologyChanged()
		return
	}

	steps := []struct {
		label string
		op    string
	}{
		// ⚠️ 必须 reload（Stop+Start），不能是 start：
		// Start() 在驱动已在跑时会短路（先查 IsProcessAlive），而 gnb 的
		// HotReload 是 false（见 driver/gnb.go 的 Capabilities）→
		// **配置改了但完全没生效**。实测撞到：p6-B 的 route.conf 里已有新对端，
		// 而 gnb_ctl 的对端列表里根本没有它 —— gnb 进程 etimes 显示是
		// 17 分钟前起的，配置 1 分钟前才改。症状是「关系建好了、配置写了、
		// 而隧道不通」，而日志里三步都是「成功」。
		{"重载驱动", commands.OpServiceReload},
		{"装内核路由", commands.OpRouteUpdate},
	}
	run := func() error {
		for _, s := range steps {
			req := commands.Request{Op: s.op}
			// service reload 不带 name：执行器会从 bundle 的 driver 字段取，
			// 守护进程不该硬编码驱动类型
			res := commands.Run(d.exec, req, d.nodeID)
			if res.OK {
				d.logf("event: bundle_changed → %s（%s）成功", s.label, s.op)
				if s.op == commands.OpServiceReload {
					recordReloadFingerprint(fp)
				}
				continue
			}
			d.logf("event: bundle_changed → %s（%s）失败: %s%s",
				s.label, s.op, res.Error, stderrSuffix(res.Stderr))
		}
		return nil
	}
	// 与 connect/config 写面互斥：拿不到锁说明用户正在启停/改配置，
	// 事件侧**让路**（用户操作自己会 apply+start），绝不并行抢驱动锁。
	if d.tryConfigExclusive != nil {
		if !d.tryConfigExclusive(run) {
			d.logf("event: bundle_changed → 有配置操作进行中，跳过 reload/route（以用户操作为准）")
		}
		return
	}
	_ = run()
	d.notifyTopologyChanged()
}

// runLifecycleStep 单步执行（跳过 reload 时刷路由用）。
func (d *eventDispatcher) runLifecycleStep(label, op string) {
	res := commands.Run(d.exec, commands.Request{Op: op}, d.nodeID)
	if res.OK {
		d.logf("event: bundle_changed → %s（%s）成功", label, op)
		return
	}
	d.logf("event: bundle_changed → %s（%s）失败: %s%s", label, op, res.Error, stderrSuffix(res.Stderr))
}

// driverConfFingerprint 驱动投影指纹（node.conf/route.conf/address.conf 等）。
// 空串 = 算不出来（保守：调用方不得据此跳过 reload）。
func driverConfFingerprint() string {
	dir, err := utils.ActiveNodeRuntimeDir()
	if err != nil {
		return ""
	}
	names := []string{"node.conf", "route.conf", "address.conf", "proxy.conf"}
	h := sha256.New()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			// 文件缺失也参与指纹（有↔无是真实变更）
			h.Write([]byte(n + "!\n"))
			continue
		}
		h.Write([]byte(n + "\n"))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func lastReloadFingerprintFile() string {
	return filepath.Join(utils.VarDir(), "last-reload-fingerprint")
}

func lastReloadFingerprint() string {
	b, err := os.ReadFile(lastReloadFingerprintFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func recordReloadFingerprint(fp string) {
	if fp == "" {
		return
	}
	_ = os.WriteFile(lastReloadFingerprintFile(), []byte(fp+"\n"), 0o644)
}

// driverLikelyRunning 粗判驱动进程是否在跑（只看 pid 文件，不 fork）。
// 用于「配置未变」时决定要不要保住当前隧道；拿不准时返回 false（走 reload，保守）。
func (d *eventDispatcher) driverLikelyRunning() bool {
	dir, err := utils.ActiveNodeRuntimeDir()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(dir, "gnb.pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	// signal 0：进程存在性探测，不发信号
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// runCommandPull 收到 command_queued 后回后端拉命令、执行、回报结果。
//
// 这是 pull 模型：后端的 GatewayNotifier 只发「有新命令待执行」的通知，
// 命令内容要节点自己取（`GET /api/v1/node/commands`，后端侧 docblock 明写
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
					d.logf("event: command_queued（command_id=%s）但后端无待执行命令"+
						"（可能已被别的连接取走或已过期）", commandID)
				}
				return
			}

			for _, cmd := range batch {
				d.executeOne(ctx, cmd)
			}
		}
		d.logf("event: 单次 command_queued 处理已达上限 %d 条，剩余仍在后端队列里，"+
			"下次 command_queued 事件或重连补拉会接着取", maxCommandsPerEvent)
	})
}

// executeOne 执行一条命令并回报结果。
//
// 映射不了 op 时**照样回报失败**，不能静默丢弃：后端侧那条命令已被标记为
// 「执行中」，不回报就会永远卡在那个状态（CPI-4 的教训：静默失效最难查）。
func (d *eventDispatcher) executeOne(ctx context.Context, cmd pendingCommand) {
	id := cmd.ID.String()

	op, ok := commands.OpFromBackCommand(cmd.Command)
	if !ok {
		d.logf("command: 后端下发了 agent 不认识的命令 %q（id=%s）—— 按失败回报，不静默忽略",
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
		// 回报失败必须吵出来：命令执行了但后端不知道，会一直显示「执行中」。
		d.logf("command: 结果回报失败（id=%s）: %v", res.ID, err)
	}
}

// runExclusive 在单飞守卫下跑一个动作，**忙碌时不丢事件**。
//
// 为什么不是「直接丢弃」：丢弃的前提是同一次在跑的动作能看到新状态。这个前提
// 在两类场景下都不成立 ——
//  1. 在跑的是 command_queued（拉命令 + 执行，可能几分钟），此时 bundle_changed
//     到达被丢 → 新配置永远不落地，除非网关再推一次（它不会）；
//  2. 在跑的 bundle 拉取已经**越过**了拉取步骤，之后的推送若被丢，本地文件不会更新。
//
// 所以改成「按事件名合并补跑」：同名只记一份，当前动作结束后再跑一次。
// 合并仍然安全（语义是「去处理」而非「处理 N 次」），但不再丢失「有变化」这个事实。
func (d *eventDispatcher) runExclusive(name string, fn func()) {
	if !d.inFlight.CompareAndSwap(false, true) {
		d.queuePending(name, fn)
		d.logf("event: %s 已有动作在跑，合并补跑", name)
		return
	}
	go func() {
		fn()
		for {
			// 守卫仍在手上时取空队列：期间新到的事件只会入队，不会并发跑。
			tasks := d.takePending()
			for _, t := range tasks {
				t.fn()
			}
			if len(tasks) == 0 {
				d.inFlight.Store(false)
				// 置空闲与上一条入队可能交错（它先入队、我们后置空闲），
				// 那一条就没有人去跑了。再确认一次，非空就重新占位继续。
				if d.hasPending() && d.inFlight.CompareAndSwap(false, true) {
					continue
				}
				return
			}
		}
	}()
}

func (d *eventDispatcher) queuePending(name string, fn func()) {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	if d.pending == nil {
		d.pending = make(map[string]func())
	}
	// 同名只留一份：合并的语义是「这个事件类型还需要被处理一次」。
	d.pending[name] = fn
}

type pendingTask struct {
	name string
	fn   func()
}

// takePending 取空补跑队列（按名字排序，保证日志与测试的确定性）。
func (d *eventDispatcher) takePending() []pendingTask {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	if len(d.pending) == 0 {
		return nil
	}
	out := make([]pendingTask, 0, len(d.pending))
	for name, fn := range d.pending {
		out = append(out, pendingTask{name: name, fn: fn})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	d.pending = nil
	return out
}

func (d *eventDispatcher) hasPending() bool {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	return len(d.pending) > 0
}

// stringifyParams 把后端的 params（任意 JSON 值）压成 commands.Request 要的
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
