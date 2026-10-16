package main

// 托盘菜单与事件循环。
//
// 菜单面（P4 收敛后）：
//
//	状态：<状态一言之>       ← 只读，显示状态机
//	连接隧道 / 断开隧道      ← **同一项**，文案与可点性都出自 FSM（CanStart/CanStop/Busy）
//	复制虚拟 IP              ← 只在 overlay_ip 合法时出现
//	打开面板                 ← 本机 WebUI 概览（/ui/）
//	查看诊断                 ← 同一个 WebUI 的 #diag 页（诊断与日志）
//	登录时启动 ✓
//	退出托盘
//
// 配置的导入/导出**不在托盘**：备份走 WebUI（浏览器写自己的下载目录，
// 不需要托盘以登录用户身份去碰 root 的系统级 home）。裁决与理由见
// docs/TRAY_T0_BASELINE.md §6 D13。
//
// 轮询：每 3s 探一次本机 API，事件喂给 FSM；FSM 发出的 Action 再去打
// mynetd 的 command 面（只控隧道，**不启停 mynetd**）。
// UI 不自己判状态（判据只在 fsm.go）。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
)

const (
	pollInterval = 3 * time.Second
	startWait    = 20 * time.Second
	stopWait     = 10 * time.Second
	// cmdTimeout 必须**长于** daemon 侧的执行上限（localapi 给执行器 60s ctx）。
	// 客户端先放弃而服务端还在跑，会让托盘状态与真实结果脱节；
	// 且「客户端超时」不等于「服务端已取消」，不能据此重试。
	cmdTimeout = 90 * time.Second
	// maxCmdBody 成功分支同样有界读取（旧版只在错误分支限 200B）。
	maxCmdBody = 1 << 20
	// noticeTTL 一次性提示的存活时间 = 两个轮询周期。
	// 短于一个周期会被下一次探针直接抹掉（等于没显示），
	// 长于两个周期又会让过期提示赖在 tooltip 上冒充现状。
	noticeTTL = 2 * pollInterval
)

// 本机 API 的两个写面。**信封不同**，不能共用一套解析：
//
//	commandPath → {success, data:{argv,stdout,stderr,bin}}（handlers.go:164）
//	connectPath → 顶层 {success, stage, message, retryable, code?, need_login?}
//	                                              （connect_api.go:39-121）
const (
	commandPath = "/api/v1/command"
	connectPath = "/api/v1/config/connect"
)

// msgUnparsable 响应不可解析时的兜底。不含 body 原文（D7）。
const msgUnparsable = "本机服务返回了不可识别的响应"

// autoTitleUnknown 自启状态查不到时的菜单文案。
// 不用「登录时启动」（未勾选）代替 —— 那会被读成「已关闭」，是个未经
// 验证的结论（D10）。
//
// 用「登录时」而不是「开机」：我们登记的是 LaunchAgent / HKCU·HKLM 的 Run，
// 都在**用户登录**时触发，不是引导时（P4 文案明确）。
const autoTitleUnknown = "登录时启动（状态未知）"

// errInProgress 409：daemon 的 configMutation 锁已被占（connect_api.go:10-23）。
// 服务端给 retryable=true + Retry-After: 2，但托盘**不自动重试** ——
// 用户没要求的启停别替他做，尤其连接是有副作用的写操作。
var errInProgress = errors.New("已有操作正在进行，请稍后再试")

// App 把 FSM + 探针 + 本机 API 客户端 + 菜单绑在一起。
//
// 并发模型：四个 goroutine 同时活着（bootSequence、3s 轮询 loop、clickLoop、
// 以及每次启停派生的 worker），它们共写 fsm / deadline / lastOneLine。
// 一把 mu 把这块状态收拢，配两条纪律：
//
//   - **绝不持锁做网络 / CLI / 文件对话框**：探针、startTunnel、saveIntent
//     全在锁外，否则一次 90s 的命令超时能把整个托盘冻住；
//   - **锁内算快照、锁外推 systray**：原生调用在 darwin 是 cgo，会派发到主线程，
//     不能压在状态锁里；顺序由 renderSeq/pushedSeq 保证「新快照不被旧的覆盖」。
//
// mu 不可重入 —— 任何持锁路径都不得调 render()/refreshOnce()。
type App struct {
	fsm  *FSM
	prob Prober
	base string // 面板 URL 的 host:port
	hc   *http.Client

	mStatus *systray.MenuItem
	// mToggle 连接/断开合并成一项（P4）：两项时用户要先读状态行才知道该点哪个，
	// 而且启停过渡期两项都灰着，菜单看起来像坏了。
	mToggle *systray.MenuItem
	mCopyIP *systray.MenuItem
	mPanel  *systray.MenuItem
	mDiag   *systray.MenuItem
	mAuto   *systray.MenuItem
	mQuit   *systray.MenuItem

	autoStart AutoStarter

	mu sync.Mutex // 保护下面这些字段（以及 FSM 内部状态）
	// deadline Starting/Stopping 的超时判据。
	deadline time.Time
	// lastOneLine 最近一次探针拿到的 node-status one_line。
	// ⚠️ daemon 原文，**未经清洗**；只有渲染路径消费它，且必须过 safeText（D8/D20）。
	lastOneLine string
	// overlayIP 最近一次探针拿到的虚拟 IP，**已过 validOverlayIP**：
	// 空串就是「没有/不合法」，菜单项直接隐藏。存校验后的值而不是 wire 原值，
	// 是为了让「显示」和「复制」用同一份判据 —— 否则会出现菜单挂着 IP、
	// 点了却报非法。
	overlayIP string
	// opSeq 单调递增的操作序号。worker 回来时先核对它是否仍是最新，
	// 不是就**整个丢弃**自己的结果 —— 否则一次迟到的「连接成功」会把用户
	// 已经切到「断开」的 deadline 重写成 startWait，把旧操作复活。
	opSeq uint64
	// noticeText / noticeUntil 一次性提示（自启切换结果、意图保存失败等）。
	//
	// **不直接 SetTooltip**：tooltip 每 3s 被探针重画，直接写会立刻被冲掉，
	// 用户点了却什么都看不到（P3:114「点击失败可见」）。存进状态、由渲染路径
	// 带上、到点自动消失，才留得住。
	noticeText  string
	noticeUntil time.Time
	// autoTitle 「登录时启动」菜单项的真实文案，由 syncAutoMenu 解析后写入。
	// 存起来而不是渲染时现查：AutoStarter 会 shell out（launchctl / 注册表），
	// 不能压在 mu 里。
	autoTitle string
	// renderSeq 快照序号；pushedSeq 记录已推给 systray 的最新序号。
	renderSeq uint64

	renderMu  sync.Mutex // 只保护 systray 推送，锁序恒为 mu → renderMu
	pushedSeq uint64
	// pushedIcon 已推给 systray 的图标态；iconUnset = 还没推过。
	pushedIcon iconKind
	// pushedCopyIP 已推给 systray 的虚拟 IP；"" = 当前是隐藏态。
	// 零值刚好就是 onReady 里的初始事实（菜单项建出来就 Hide），不需要哨兵。
	pushedCopyIP string

	// renderFn 渲染出口替身；nil = 生产路径 snapshot + push。
	// 存在的唯一理由是 systray 的 darwin 腿为无条件 cgo 调用
	// （SetTooltip / addOrUpdateMenuItem 不判事件循环是否已起），
	// 没有 systray.Run 就进不去 —— 换掉渲染出口，App 的逻辑才可测。
	renderFn func()

	// clipFn 剪贴板出口替身；nil = 生产路径 platformClipboard。
	// 同样是可测性接缝：测试不该往开发机的真实剪贴板里写东西，
	// 也不能因为 CI 上没有 pbcopy 就跳过「复制失败要上屏」这条分支。
	clipFn func(string) error
}

// view 一次渲染所需的一致快照。在 mu 内算好，锁外推给 systray。
type view struct {
	seq            uint64
	title, tooltip string
	toggleTitle    string
	toggleEnabled  bool
	overlayIP      string // 空 = 隐藏「复制虚拟 IP」
	autoTitle      string
	icon           iconKind
}

func NewApp(prob Prober, base string) *App {
	return &App{
		fsm:        NewFSM(),
		prob:       prob,
		base:       base,
		hc:         &http.Client{Timeout: cmdTimeout},
		pushedIcon: iconUnset,
		autoStart:  NewAutoStarter(),
	}
}

// Run 阻塞直到退出。onReady 里搭菜单。
func (a *App) Run() {
	systray.Run(a.onReady, a.onExit)
}

func (a *App) onReady() {
	// 只用 icon，不 SetTitle —— macOS 菜单栏上的标题文字很占地方。
	// 首帧按当前状态推一次并记进 pushedIcon：Run 起到第一次 render 之间
	// 若没图标，菜单栏会空一格；之后图标只随状态变化推（acceptIcon）。
	k := iconKindFor(a.fsm.State())
	a.pushedIcon = k
	systray.SetTemplateIcon(iconBytes(k, true), iconBytes(k, false))
	systray.SetTooltip("MyNet Agent")

	// 冷启动还没探过，不能先断言「已停止」—— 那等于说「服务在跑、只是隧道没连」，
	// 是个未经验证的结论（服务可能根本没装）。bootSequence 的第一次探针会立刻替换。
	a.mStatus = systray.AddMenuItem("状态：正在检查…", "")
	a.mStatus.Disable()
	systray.AddSeparator()

	// tooltip 不能写「拉起/停掉 mynetd」：托盘只控隧道，mynetd 由系统服务常驻。
	// Controller 死件撤回后（D15）这句更是彻底的假话。
	// 初值按 FSM 当前态算（此刻是 SvcDown ⇒ 禁用），bootSequence 第一次探针后替换。
	t0, en0 := toggleFor(a.fsm.State(), a.fsm.CanStart(), a.fsm.CanStop(), a.fsm.Busy())
	a.mToggle = systray.AddMenuItem(t0, "只控隧道：不启停本机服务，也不退出托盘")
	if !en0 {
		a.mToggle.Disable()
	}

	// overlay_ip 要等第一次探针，所以初始就是隐藏的；缺失/非法一直隐藏。
	a.mCopyIP = systray.AddMenuItem("复制虚拟 IP", "")
	a.mCopyIP.Hide()
	systray.AddSeparator()

	a.mPanel = systray.AddMenuItem("打开面板", "本机 WebUI：概览、节点配置与路由")
	// 诊断是**真实存在的深链**（WebUI 自带 hash 路由，#diag = 诊断与日志页），
	// 不是新造的页面；见 DiagURL 的注释。
	a.mDiag = systray.AddMenuItem("查看诊断", "打开面板的「诊断与日志」页")
	// 初值不写「登录时启动」：此刻还没查过真实状态，未勾选会被读成「已关闭」，
	// 是个未经验证的结论（D10）。bootSequence 的 syncAutoMenu 会立刻替换。
	a.mAuto = systray.AddMenuItem(autoTitleUnknown, "登录后自动拉起托盘")
	systray.AddSeparator()
	// 「退出托盘」而不是「退出」：用户要能一眼看出这一项不会断开隧道（P4 文案明确）。
	a.mQuit = systray.AddMenuItem("退出托盘", "只关掉托盘图标；隧道保持现状，本机服务不受影响")

	// 启动序：先 mynetd（本机 API），再按上次意图决定是否起节点
	go a.bootSequence()

	go a.loop()
	go a.clickLoop()
}

// bootSequence 托盘冷启动：
//
//	只探测本机 API，不主动 bootstrap（mynetd 由系统服务保证常驻）。
//	隧道是否起，看上次意图；启停一律经本机 API。
//
// 权限边界：托盘**不停 mynetd**，只停隧道。
func (a *App) bootSequence() {
	p, _ := a.prob.Probe()
	// 服务没在跑不单独写文案：下面的 fsm.On(eventFromProbe(p)) 会落 msgSvcDown。
	// 两处各写一句必然漂（§3「同义文案多份」）。
	// 也不默默提权拉起系统服务 —— 那是安装器的活。
	intent := loadIntent()

	a.mu.Lock()
	a.lastOneLine = p.OneLine
	a.overlayIP = validOverlayIP(p.OverlayIP)
	a.fsm.On(eventFromProbe(p))
	// 意图恢复只在「现在确实能起」时发：节点已在跑就别再打一次 connect。
	// 旧版无条件发 —— 既多发一次有副作用的写操作，又不进 Starting，
	// 用户在菜单上看不到任何进度。
	var seq uint64
	doStart := false
	if intent.WantNode && p.Up && p.Valid && a.fsm.CanStart() {
		if _, act, _ := a.fsm.On(EvStart); act == ActionDoStart {
			seq = a.nextOpLocked()
			doStart = true
		}
	}
	a.mu.Unlock()

	if doStart {
		go a.runStart(seq)
	}
	a.refreshOnce()
	// 自启状态在冷启动时解析一次：菜单初值是「状态未知」，不该一直挂着。
	a.syncAutoMenu()
}

// startTunnel 连接隧道 —— 打 daemon 的**专用编排端点**，不发 argv。
//
// ⚠️ 旧版发的是 `apply --active`，而 cmd_apply.go:25/33 自己写着那是
// 「投影：…（**不装 OS 路由**）」，cmdApply 全程不起隧道进程 —— 也就是说
// 「连接」按钮从来没连接过（D19）。而且它不走 service 分支，
// markTunnelWantDriver 从不触发 ⇒ var/tunnel-state.json 的 want_running
// 永远不为真，开机恢复（cmd/mynetd/tunnel_restore.go:130-151）也不会拉起。
//
// 真正的编排在守护侧已经存在：apply --active → service start <driver> →
// apply --auto，失败自动 cleanup（service stop），驱动名由服务端从配置解析，
// 并顺手记账 want_running/driver。托盘只按端点，不把命令链搬进来。
// 顺带继承：409 互斥锁、need_login→401、上游脱敏。
func (a *App) startTunnel() error {
	code, body, err := a.post(connectPath, nil)
	if err != nil {
		return err
	}
	if code == http.StatusConflict {
		return errInProgress
	}
	var env struct {
		Success   bool   `json:"success"`
		Stage     string `json:"stage"`
		Message   string `json:"message"`
		Code      string `json:"code"`
		NeedLogin bool   `json:"need_login"`
	}
	if uerr := json.Unmarshal(body, &env); uerr != nil {
		return errors.New(msgUnparsable)
	}
	if env.Success {
		return nil
	}
	if env.NeedLogin || env.Code == "need_login" {
		return errors.New("登录已失效，请打开面板重新登录后再连接")
	}
	return apiFailure(env.Message, env.Stage)
}

// stopTunnel 断开隧道 —— `service stop`，**不写死驱动名**（D14）。
//
// 驱动参数是可选的，CLI 会从 bundle 自己解析（cmd_apply.go:173-189）。
// 旧版写死 "gnb"：wireguard 节点上停的是没在跑的 gnb，wg 隧道原样留着，
// 而托盘会显示「已断开」。handlers.go:63-67 对 argv[2] 有长度守卫，
// 两元素 argv 不越界。
func (a *App) stopTunnel() error {
	return a.postCommand("service", "stop")
}

// postCommand 打 daemon 的 command 面（由 root 的 mynetd 执行**固定** argv）。
//
// argv 一律由托盘代码写死，绝不来自响应或用户输入（§5.2）。
func (a *App) postCommand(argv ...string) error {
	payload, err := json.Marshal(map[string]any{"argv": argv})
	if err != nil {
		return err
	}
	code, body, err := a.post(commandPath, payload)
	if err != nil {
		return err
	}
	if code == http.StatusConflict {
		return errInProgress
	}
	var env struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if uerr := json.Unmarshal(body, &env); uerr != nil {
		return errors.New(msgUnparsable)
	}
	// D5：HTTP 200 + success:false 是**业务失败**。旧版只判 StatusCode>=300，
	// 于是「当前节点未获准使用出口代理」这类拒绝被当成成功，托盘报「已连接」。
	if !env.Success {
		return apiFailure(env.Message, "")
	}
	return nil
}

// post 发一次本机 API 请求，返回状态码与**有界**读取的 body。
func (a *App) post(path string, payload []byte) (int, []byte, error) {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+a.base+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Origin 校验（安全红线：embed WebUI 必须校验 Origin/Host）。
	req.Header.Set("Origin", "http://"+a.base)
	resp, err := a.hc.Do(req)
	if err != nil {
		// 不拼底层 err（可能含 URL/地址）；这句是安全文案。
		return 0, nil, errors.New(msgSvcDown)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxCmdBody))
	return resp.StatusCode, body, nil
}

// apiFailure 把 daemon 的 message 变成可展示的安全文案。
//
// ⚠️ 不拼 HTTP body 原文、不拼 stderr、不拼路径（D7）。message 本身也必须
// 过清洗：connect_stage_failed 的实测取证表明它会带**未遮蔽**的私钥指纹与
// 绝对路径（daemon 的 redactSecrets 只认英文关键字，D20）。
func apiFailure(message, stage string) error {
	if msg := safeText(message, MaxMenuRunes*2); msg != "" {
		return errors.New(msg)
	}
	if stage != "" {
		return fmt.Errorf("操作在「%s」阶段失败", safeText(stage, MaxMenuRunes))
	}
	return errors.New("本机服务拒绝了该操作")
}

func (a *App) onExit() {
	// 托盘退出**不停**节点 —— 用户点的是「退出」，不是「卸载」。
}

func (a *App) clickLoop() {
	for {
		select {
		case <-a.mToggle.ClickedCh:
			a.onUserToggle()
		case <-a.mCopyIP.ClickedCh:
			a.copyOverlayIP()
		case <-a.mPanel.ClickedCh:
			a.openPanel()
		case <-a.mDiag.ClickedCh:
			a.openDiag()
		case <-a.mAuto.ClickedCh:
			a.toggleAutoStart()
		case <-a.mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// nextOpLocked 领取一个新的操作序号，同时作废所有在途 worker 的结果。
// 调用方必须持 mu。
func (a *App) nextOpLocked() uint64 {
	a.opSeq++
	return a.opSeq
}

// applyOpResult 只有当 seq 仍是最新一次操作时才执行 fn。
//
// 迟到的 worker 结果必须**整个丢弃**：一次晚到的「连接成功」会把用户已经切到
// 「断开」的 deadline 重写成 startWait，把旧操作复活；一次晚到的报错会盖掉
// 新操作刚写下的原因。fn 在锁内跑，因此只许写状态，不得碰网络/CLI/systray。
func (a *App) applyOpResult(seq uint64, fn func()) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if seq != a.opSeq {
		return false
	}
	fn()
	return true
}

// failOp worker 的失败/panic 出口，同样过序号核对。
func (a *App) failOp(seq uint64, ev Event, format string, args ...any) {
	msg := safeText(fmt.Sprintf(format, args...), MaxMenuRunes*2)
	if !a.applyOpResult(seq, func() {
		a.fsm.lastErr = msg
		a.fsm.On(ev)
	}) {
		return
	}
	a.render()
}

// onUserToggle 合并后连接/断开项的唯一点击入口。
//
// 判据必须和 snapshot 给菜单的那份**同源**（都出自 FSM 的 CanStart/CanStop/Busy），
// 否则会出现「菜单写着断开、点了却去连接」。Busy 时菜单是禁用的，但点击
// channel 仍可能被投递（合成事件 / 用户狂点），这里再挡一道：进度期间绝不
// 重复发有副作用的写操作。
//
// 释锁后再调 onUserStart/onUserStop 是有意的：两者各自在锁内复检 CanStart/
// CanStop，状态若在窗口里变了就自然变成 no-op，不会发错命令。
func (a *App) onUserToggle() {
	a.mu.Lock()
	busy := a.fsm.Busy()
	canStart := a.fsm.CanStart()
	a.mu.Unlock()

	switch {
	case busy:
		return
	case canStart:
		a.onUserStart()
	default:
		a.onUserStop()
	}
}

func (a *App) onUserStart() {
	a.mu.Lock()
	if !a.fsm.CanStart() {
		a.mu.Unlock()
		return
	}
	_, act, _ := a.fsm.On(EvStart)
	var seq uint64
	do := act == ActionDoStart
	if do {
		seq = a.nextOpLocked()
	}
	a.mu.Unlock()

	if do {
		// 记住「用户要节点在跑」。文件写也在锁外 —— 磁盘 IO 不压状态锁。
		// 旧版 `_ =` 把错误丢掉，于是「记住意图」在只读目录下静默失效（D9）。
		if err := saveIntent(trayIntent{WantNode: true}); err != nil {
			a.notice("意图保存失败：重启托盘后不会自动恢复连接")
		}
		go a.runStart(seq)
	}
	a.render()
}

// runStart 连接 worker：网络调用在锁外，回来后过 applyOpResult 核对序号。
func (a *App) runStart(seq uint64) {
	defer func() {
		if r := recover(); r != nil {
			a.failOp(seq, EvStartFailed, "连接异常: %v", r)
		}
	}()
	// 服务不可用时 startTunnel 自己会失败并给出安全文案，
	// 不必在这里先探一次（少一条会漂的判据）。
	err := a.startTunnel()
	if !a.applyOpResult(seq, func() {
		if err != nil {
			a.fsm.lastErr = safeText(err.Error(), MaxMenuRunes*2)
			a.fsm.On(EvStartFailed)
			return
		}
		// 命令成功 ≠ 隧道健康：等探针在 startWait 内报出真实事实，
		// 到点没等到由 refreshOnce 判 EvStartTimeout。
		a.deadline = time.Now().Add(startWait)
	}) {
		return
	}
	a.render()
}

func (a *App) onUserStop() {
	a.mu.Lock()
	if !a.fsm.CanStop() {
		a.mu.Unlock()
		return
	}
	_, act, _ := a.fsm.On(EvStop)
	var seq uint64
	do := act == ActionDoStop
	if do {
		seq = a.nextOpLocked()
	}
	a.mu.Unlock()

	if do {
		// 记住「用户要隧道停着」；**不停 mynetd**
		if err := saveIntent(trayIntent{WantNode: false}); err != nil {
			a.notice("意图保存失败：重启托盘后可能自动恢复连接")
		}
		go a.runStop(seq)
	}
	a.render()
}

func (a *App) runStop(seq uint64) {
	defer func() {
		if r := recover(); r != nil {
			a.failOp(seq, EvStopFailed, "断开异常: %v", r)
		}
	}()
	err := a.stopTunnel()
	if !a.applyOpResult(seq, func() {
		if err != nil {
			// 命令都没发出去/被拒：不进「正在断开」等待，直接落异常并报原因。
			// 旧版无论成败都设 deadline，失败原因被 stopWait 后的超时文案覆盖。
			a.fsm.lastErr = safeText(err.Error(), MaxMenuRunes*2)
			a.fsm.On(EvStopFailed)
			return
		}
		a.deadline = time.Now().Add(stopWait)
	}) {
		return
	}
	a.render()
}

func (a *App) loop() {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for range t.C {
		a.refreshOnce()
	}
}

func (a *App) refreshOnce() {
	// 探针在锁外：2s 超时的 HTTP 不能压在状态锁上，否则点击处理会被冻住。
	p, err := a.prob.Probe()
	if err != nil {
		// 探针自身故障不该让托盘卡死；按「服务不可用」处理
		p = Probe{Up: false}
	}

	a.mu.Lock()
	// 探不到就清空，别把上一轮的 one_line / overlay_ip 留在界面上冒充现状
	a.lastOneLine = p.OneLine
	a.overlayIP = validOverlayIP(p.OverlayIP)
	ev := eventFromProbe(p)

	// 过渡态超时：探针还没反映结果时，到点就判失败。
	// 只在 lastErr 为空时落超时文案，别盖掉已知的真实原因（§3）。
	if a.fsm.Busy() && !a.deadline.IsZero() && time.Now().After(a.deadline) {
		if a.fsm.State() == StateStarting {
			ev = EvStartTimeout
		} else {
			ev = EvStopTimeout
		}
		a.deadline = time.Time{}
	}

	a.fsm.On(ev)
	a.mu.Unlock()
	// 稳态也要刷：one_line 每轮都可能变（旧版这里 if/else 两支都调 render，
	// 判据是假的，直接合并）。
	a.render()
}

// render 刷新界面。测试用 renderFn 换掉出口（见 App.renderFn 注释）。
func (a *App) render() {
	if a.renderFn != nil {
		a.renderFn()
		return
	}
	a.push(a.snapshot())
}

// snapshot 在 mu 内算出一份自洽的渲染数据（状态、文案、可点性同源）。
func (a *App) snapshot() view {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.renderSeq++
	title, tooltip := a.statusTextLocked()
	autoTitle := a.autoTitle
	if autoTitle == "" {
		autoTitle = autoTitleUnknown
	}
	toggleTitle, toggleEnabled := toggleFor(a.fsm.State(), a.fsm.CanStart(), a.fsm.CanStop(), a.fsm.Busy())
	return view{
		seq:           a.renderSeq,
		title:         title,
		tooltip:       tooltip,
		toggleTitle:   toggleTitle,
		toggleEnabled: toggleEnabled,
		overlayIP:     a.overlayIP,
		autoTitle:     autoTitle,
		icon:          iconKindFor(a.fsm.State()),
	}
}

// toggleFor 合并项的文案与可点性。纯函数，判据只出自 FSM。
//
// Failed 同时满足 CanStart 与 CanStop —— 取「连接」（重试）：连接异常时用户
// 要的是恢复；真想断开的场景多半是隧道进程还赖着，而进程一没，下一轮探针
// 就把状态落到 Stopped，这一项自己就变成「连接隧道」，不会少给用户能力。
//
// 过渡态用状态机自己的进行时文案（「正在连接…」）并禁用：菜单不许在进度期间
// 再发一次写操作，也不许用一个静止的动词冒充正在进行的事。
//
// SvcDown 落到 default：文案保持「连接隧道」但禁用 —— 状态行已经写明
// 「本机服务不可用」，不需要第二处表达同一个事实。
func toggleFor(st State, canStart, canStop, busy bool) (title string, enabled bool) {
	switch {
	case busy:
		return st.Label(), false
	case canStart:
		return "连接隧道", true
	case canStop:
		return "断开隧道", true
	default:
		return "连接隧道", false
	}
}

// acceptPush 最新胜出：慢的那份快照不得覆盖已经推上去的新状态。
//
// 可达场景是真实的 —— render() = push(snapshot())，snapshot 与 push 用的是
// 两把锁，所以 G1 拿到 seq=5 之后、还没推之前，G2 完全可以拿 seq=6 并推完；
// 没有这道守卫，界面就会停在 seq=5 那份旧状态上（最长一个轮询周期）。
//
// 单独抽出来是为了**可测**：push 里的 systray 调用在 darwin 是无条件 cgo，
// 没有事件循环就崩，所以顺序判定必须能脱离 cgo 被并发驱动。
func (a *App) acceptPush(seq uint64) bool {
	a.renderMu.Lock()
	defer a.renderMu.Unlock()
	if seq < a.pushedSeq {
		return false
	}
	a.pushedSeq = seq
	return true
}

// acceptIcon 图标只在态变了才推。
//
// 跟着 3s 轮询每轮白推一次没有意义：Windows 那条腿每次 SetIcon 都要 md5 +
// stat + 一次 Shell_NotifyIcon（临时文件按内容哈希命名、缺失才写，不会堆积，
// 但通知是实打实的系统调用）；darwin 那条腿是一次主线程派发。
func (a *App) acceptIcon(k iconKind) bool {
	a.renderMu.Lock()
	defer a.renderMu.Unlock()
	if k == a.pushedIcon {
		return false
	}
	a.pushedIcon = k
	return true
}

// acceptCopyIP 同 acceptIcon：「复制虚拟 IP」的显隐只在值变了才推。
func (a *App) acceptCopyIP(ip string) bool {
	a.renderMu.Lock()
	defer a.renderMu.Unlock()
	if ip == a.pushedCopyIP {
		return false
	}
	a.pushedCopyIP = ip
	return true
}

// push 把快照推给 systray。**mu 锁外**执行：darwin 腿是无条件 cgo，会派发到
// 主线程，压在状态锁里等于让所有状态读写等一次 UI 往返。
func (a *App) push(v view) {
	if !a.acceptPush(v.seq) {
		return
	}
	// 只改菜单项与 tooltip，**不** systray.SetTitle
	// （macOS 菜单栏上的标题文字很占地方）。
	a.mStatus.SetTitle(v.title)
	a.mStatus.SetTooltip(v.tooltip)
	a.mAuto.SetTitle(v.autoTitle)
	systray.SetTooltip(v.tooltip)

	if a.acceptIcon(v.icon) {
		systray.SetTemplateIcon(iconBytes(v.icon, true), iconBytes(v.icon, false))
	}

	a.mToggle.SetTitle(v.toggleTitle)
	if v.toggleEnabled {
		a.mToggle.Enable()
	} else {
		a.mToggle.Disable()
	}

	// overlay_ip 是配置面的值，几乎从不变；变了才动菜单（Hide/Show 在 darwin
	// 是一次主线程派发，Windows 是一次 Shell_NotifyIcon 级别的更新）。
	// v.overlayIP 已由 validOverlayIP 规范成 net.IP.String()，是纯 ASCII，
	// 不再过 safeText。
	if a.acceptCopyIP(v.overlayIP) {
		if v.overlayIP == "" {
			a.mCopyIP.Hide()
		} else {
			a.mCopyIP.SetTooltip("复制 " + v.overlayIP + " 到剪贴板")
			a.mCopyIP.Show()
		}
	}
}

// notice 一次性提示（自启切换结果、意图保存失败等），活 noticeTTL 后自动消失。
//
// 存进状态、由渲染路径带上，而不是直接 SetTooltip：tooltip 每 3s 被探针重画，
// 直写会立刻被冲掉，用户点了却什么都看不到（P3:114「点击失败可见」）。
// 文案固定不带 OS 原始 error —— 里面常是绝对路径，既不脱敏又会被 clamp 截断。
func (a *App) notice(text string) {
	a.mu.Lock()
	a.noticeText = safeText(text, MaxMenuRunes*2)
	a.noticeUntil = time.Now().Add(noticeTTL)
	a.mu.Unlock()
	a.render()
}

// statusText 算出状态行标题与 tooltip。加锁包装，供测试直接调。
func (a *App) statusText() (title, tooltip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.statusTextLocked()
}

// statusTextLocked 纯计算，不碰 systray。调用方必须持 mu。
func (a *App) statusTextLocked() (title, tooltip string) {
	st := a.fsm.State()

	// 菜单标题只放状态短语：它会被 clamp 到 MaxMenuRunes，把原因拼进去
	// 等于把长文案截成半句，还占满常显的菜单行。原因进 tooltip。
	title = safeText("状态："+st.Label(), MaxMenuRunes)

	// 出 UI 的文本一律过 safeText：one_line 与 daemon message 实测可带换行、
	// BEL/ESC 控制字符、未遮蔽的私钥指纹与绝对路径（取证 fixture
	// status_secret_injection.json / connect_stage_failed.json）。
	var detail []string
	// 提示排最前：它是刚刚那次点击的直接反馈，比常态信息更该被看见。
	if a.noticeText != "" && time.Now().Before(a.noticeUntil) {
		detail = append(detail, a.noticeText)
	}
	if one := safeText(a.lastOneLine, MaxMenuRunes*2); one != "" {
		detail = append(detail, one)
	}
	// 只在坏状态显示原因：健康态留着上一轮的报错会误导。
	if st == StateFailed || st == StateSvcDown {
		if e := safeText(a.fsm.LastError(), MaxMenuRunes*2); e != "" {
			detail = append(detail, e)
		}
	}
	tooltip = title
	if len(detail) > 0 {
		tooltip = title + " · " + strings.Join(detail, "；")
	}
	return title, tooltip
}

func (a *App) openPanel() { a.openURL(PanelURL(a.base)) }

// openDiag 直接落在面板的「诊断与日志」页。
//
// #diag 不是我编的深链：WebUI 自带 hash 路由（index.html:1429-1435 的 PAGES、
// :1481-1486 的 parseHash、:3167 启动即调用），#diag 就是那一页。
// 页面键改了这条就会静默退回概览页 —— TestDiagURLMatchesWebUIPage 盯着这件事。
func (a *App) openDiag() { a.openURL(DiagURL(a.base)) }

func (a *App) openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// validOverlayIP 校验并规范化虚拟 IP。非法/缺失返回 ""（消费方据此隐藏菜单项）。
//
// 为什么必须自己验：overlay_ip 出自配置面（route.conf 本机行 → bundle →
// 旧 mynet.conf，mynet/node_status.go:521-539），是历史遗留字符串也能透出来，
// 不是从活接口读的事实。复制一个非法值进剪贴板，比不显示更糟（§8.1「不复制旧 IP」）。
// 规范化用 net.IP.String()：进剪贴板的是压缩后的规范写法，不是配置里的原样文本。
func validOverlayIP(s string) string {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return ""
	}
	return ip.String()
}

// copyOverlayIP 用户点「复制虚拟 IP」。
//
// 三条纪律（playbook P4 / §5.2）：
//   - 只认结构化字段 overlay_ip，**不解析 OneLine** —— 那是一句给人看的中文，
//     从里面抠 IP 等于造第二套判据，必然漂；
//   - 只在用户点击时写剪贴板，**从不读**剪贴板；
//   - 成败都上屏：点了没反应和「静默失败」是同一件事（P3:114 同一条纪律）。
func (a *App) copyOverlayIP() {
	a.mu.Lock()
	ip := a.overlayIP
	a.mu.Unlock()

	if ip == "" {
		// 菜单项此刻是隐藏的，正常点不到；真收到了就如实说没有。
		a.notice("当前没有可复制的虚拟 IP")
		return
	}
	write := a.clipFn
	if write == nil {
		write = platformClipboard
	}
	if err := write(ip); err != nil {
		// 不拼底层 err（含可执行文件路径）；固定短句（D20）。
		a.notice("复制到剪贴板失败")
		return
	}
	a.notice("已复制虚拟 IP " + ip)
}

// platformClipboard 写系统剪贴板。托盘只出 darwin / windows 两条腿
// （Makefile:147-153），所以这里也只实现这两条，其余如实报错，
// 不去猜 xclip / wl-copy 哪个在（那是没被构建、也没被验证过的路径）。
func platformClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "windows":
		// clip.exe 原样把 stdin 送进剪贴板。text 是 net.IP.String()，
		// 只含 ASCII，不涉及编码转换。
		cmd = exec.Command("clip")
	default:
		return errors.New("该平台不支持复制")
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func (a *App) toggleAutoStart() {
	on, err := a.autoStart.IsEnabled()
	if err != nil {
		// 读不到就别猜着切：按 err 当「关着」去 Enable，可能把已经开着的东西
		// 再开一遍（注册表/launchctl 重复项），而且失败对用户完全不可见（D10）。
		a.notice("读取登录时启动状态失败")
		return
	}
	if on {
		err = a.autoStart.Disable()
	} else {
		err = a.autoStart.Enable()
	}
	if err != nil {
		a.notice(autoFailText(on, err))
	}
	// 成败都重读：失败也可能已经改了状态（Disable 会先删掉用户级那份、
	// 再报系统级仍在位），而菜单显示的必须是系统的真实状态，
	// 不是「我们刚请求的意图」。
	a.syncAutoMenu()
}

// autoFailText 自启切换失败的上屏文案。
//
// 我们自己造的错误是安全文案，原样上屏（errSystemManaged 得告诉用户
// 为什么改不了）；平台命令的 error 含 launchctl/reg 的原始输出与绝对路径，
// 只给固定短句（D20）。
func autoFailText(wasOn bool, err error) string {
	if errors.Is(err, errSystemManaged) {
		return err.Error()
	}
	if wasOn {
		return "关闭登录时启动失败"
	}
	return "开启登录时启动失败"
}

// syncAutoMenu 解析真实自启状态并触发一次渲染。
//
// AutoStarter 会 shell out（launchctl / 注册表），所以读在 mu 外；
// 结果存进 autoTitle，由 snapshot/push 走统一渲染出口 —— 直接 SetTitle
// 会和 push 并发写 systray，且在 darwin 是无条件 cgo（测试进不去）。
func (a *App) syncAutoMenu() {
	on, err := a.autoStart.IsEnabled()
	title := autoTitleFor(on, err)
	a.mu.Lock()
	a.autoTitle = title
	a.mu.Unlock()
	a.render()
}

// autoTitleFor 把「读得到吗 + 开着吗」翻成菜单文案。纯函数，可单测。
//
// D10/D11：读不到状态就明说「状态未知」，**不用「未勾选」冒充「已关闭」**——
// 那是一个未经验证的结论（与冷启动不得先断言「已停止」同一条纪律）。
// 旧版 `on, _ := IsEnabled()` 把 err 丢掉，于是 launchctl 查询失败时
// 菜单显示「未勾选」，用户再点一次会去 Enable 一个可能已经开着的东西。
//
// 用「登录时启动」而不是「开机启动」：登记的是 LaunchAgent / Run 键，
// 触发点是用户登录，不是引导（P4 文案明确）。
func autoTitleFor(on bool, err error) string {
	switch {
	case err != nil:
		return autoTitleUnknown
	case on:
		return "登录时启动 ✓"
	default:
		return "登录时启动"
	}
}

// 配置导入/导出已从托盘移除（D13 改判，见 docs/TRAY_T0_BASELINE.md §6）。
//
// 阻塞点是**读 root 的目录**，不是「托盘不能写盘」。分开看：
//
//	导出：要读 conf/**/security/<nodeId>.private（0600，在 0700 的 conf/ 下），
//	      而 conf/ 在 root 的系统级 home（main.go:41-50 定 /opt/mynet，
//	      utils.PlatformDefaultHome 注释自陈「系统级唯一，不设用户级模式」，
//	      全仓零 chown）⇒ 以登录用户身份跑的托盘**读不出来**。
//	      写盘本身没问题：托盘写登录用户自己的下载目录很自然。
//	导入：要写回 root 的 conf/ ⇒ 登录用户**写不进去**；真正该落盘的是 daemon。
//	旧版还把失败静默 return，用户点了没反应也看不出为什么。
//
// 正解在 daemon 侧：包体只有几百字节（cmd_config.go:44-47 已排除可再生的
// driver/** 投影），导出=内存 tar 走响应流、导入=浏览器上传走专用端点，
// 既不碰 -o 任意路径（那会让未鉴权调用方指挥 root 截断覆写任意文件），
// 也不需要把密钥落盘再想办法取回。

// AutoStarter 开机自启。
type AutoStarter interface {
	IsEnabled() (bool, error)
	Enable() error
	Disable() error
}

// PanelURL 拼面板地址。**必须是 /ui/**：根路径返回的是 JSON 壳，
// 浏览器打开只会看到一坨 JSON。面板在 /ui/（embed）或 /gui/（Quasar 控制台）。
func PanelURL(base string) string {
	return "http://" + base + "/ui/"
}

// diagHash 诊断页在 WebUI hash 路由里的页面键（index.html:1433 PAGES.diag）。
const diagHash = "diag"

// DiagURL 面板的「诊断与日志」页。基于 PanelURL 拼，不另写一份地址（§3）。
func DiagURL(base string) string {
	return PanelURL(base) + "#" + diagHash
}

// selfPath 自身可执行文件路径（写自启用）。
func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}
