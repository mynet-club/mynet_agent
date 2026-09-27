package main

// 托盘菜单与事件循环。
//
// 菜单面（用户点名的四件事 + 状态行）：
//
//	MyNet · <状态一言之>     ← 只读，显示状态机
//	启动 / 停止              ← 按状态机 CanStart/CanStop 禁用
//	进入面板
//	开机启动 ✓
//	导出配置… / 导入配置…    ← 委托 mynet config
//	退出
//
// 轮询：每 3s 探一次本机 API，事件喂给 FSM；FSM 发出的 Action
// 再去调 Controller。UI 不自己判状态（判据只在 fsm.go）。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fyne.io/systray"
)

const (
	pollInterval = 3 * time.Second
	startWait    = 20 * time.Second
	stopWait     = 10 * time.Second
)

// App 把 FSM + 探针 + 控制器 + 菜单绑在一起。
type App struct {
	fsm  *FSM
	prob Prober
	ctrl Controller
	base string // 面板 URL 的 host:port

	mStatus *systray.MenuItem
	mStart  *systray.MenuItem
	mStop   *systray.MenuItem
	mPanel  *systray.MenuItem
	mAuto   *systray.MenuItem
	mExport *systray.MenuItem
	mImport *systray.MenuItem
	mQuit   *systray.MenuItem

	autoStart AutoStarter
	// pending deadline：Starting/Stopping 超时判据
	deadline time.Time
}

func NewApp(prob Prober, ctrl Controller, base string) *App {
	return &App{
		fsm:       NewFSM(),
		prob:      prob,
		ctrl:      ctrl,
		base:      base,
		autoStart: NewAutoStarter(),
	}
}

// Run 阻塞直到退出。onReady 里搭菜单。
func (a *App) Run() {
	systray.Run(a.onReady, a.onExit)
}

func (a *App) onReady() {
	// 只用 icon，不 SetTitle —— macOS 菜单栏上的标题文字很占地方
	// macOS 用 Template（深浅菜单栏自适应）；Windows 回退彩色 iconPNG
	systray.SetTemplateIcon(iconTemplatePNG, iconPNG)
	systray.SetTooltip("MyNet Agent")

	a.mStatus = systray.AddMenuItem("状态：已停止", "")
	a.mStatus.Disable()
	systray.AddSeparator()

	a.mStart = systray.AddMenuItem("连接隧道", "拉起 mynetd")
	a.mStop = systray.AddMenuItem("断开隧道", "停掉 mynetd")
	a.mStop.Disable()
	systray.AddSeparator()

	a.mPanel = systray.AddMenuItem("进入面板", "打开本机 WebUI")
	a.mAuto = systray.AddMenuItem("开机启动", "登录后自动拉起托盘")
	systray.AddSeparator()
	a.mExport = systray.AddMenuItem("导出配置…", "备份节点身份与 conf 到 tar.gz")
	a.mImport = systray.AddMenuItem("导入配置…", "从 tar.gz 恢复（先自动备份现状）")
	systray.AddSeparator()
	a.mQuit = systray.AddMenuItem("退出", "退出托盘（不碰节点）")

	// 启动序：先 mynetd（本机 API），再按上次意图决定是否起节点
	go a.bootSequence()

	go a.loop()
	go a.clickLoop()
}

// bootSequence 托盘冷启动：
//
//	只探测 mynetd，不主动 bootstrap（mynetd 由系统服务保证常驻）。
//	隧道是否起，看 last_intent；启停一律走 VPNDriver（经 API）。
//
// 权限边界：托盘**不停 mynetd**，只停网口/隧道。
func (a *App) bootSequence() {
	p, _ := a.prob.Probe()
	if !p.Up {
		// 系统服务未运行：不默默提权拉起，交给用户决定
		a.fsm.lastErr = "本机服务未运行（请重新安装或以管理员启动 mynetd）"
		a.render()
		// 仍周期探测，服务起来后会自动接上
	}

	intent := loadIntent()
	if intent.WantNode && p.Up {
		if err := a.startTunnel(); err != nil {
			a.fsm.lastErr = err.Error()
		}
	}
	a.refreshOnce()
	a.render()
}

// startTunnel 起**隧道**（网口），经 mynetd → VPNDriver.Start，不动 mynetd。
func (a *App) startTunnel() error {
	return a.postCmd("apply", "--active")
}

// stopTunnel 停**隧道**（网口），经 mynetd → VPNDriver.Stop，不动 mynetd。
func (a *App) stopTunnel() error {
	return a.postCmd("service", "stop", "gnb")
}

// postCmd 打 mynetd 本机 API 的 command 面（由 root 的 mynetd 执行）。
func (a *App) postCmd(argv ...string) error {
	body, _ := json.Marshal(map[string]any{"argv": argv})
	req, err := http.NewRequest(http.MethodPost, "http://"+a.base+"/api/v1/command", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+a.base)
	hc := &http.Client{Timeout: 20 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("本地 API 未启动: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (a *App) onExit() {
	// 托盘退出**不停**节点 —— 用户点的是「退出」，不是「卸载」。
}

func (a *App) clickLoop() {
	for {
		select {
		case <-a.mStart.ClickedCh:
			a.onUserStart()
		case <-a.mStop.ClickedCh:
			a.onUserStop()
		case <-a.mPanel.ClickedCh:
			a.openPanel()
		case <-a.mAuto.ClickedCh:
			a.toggleAutoStart()
		case <-a.mExport.ClickedCh:
			a.exportConfig()
		case <-a.mImport.ClickedCh:
			a.importConfig()
		case <-a.mQuit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func (a *App) onUserStart() {
	if !a.fsm.CanStart() {
		return
	}
	_, act, _ := a.fsm.On(EvStart)
	if act != ActionDoStart {
		a.render()
		return
	}
	// 记住「用户要节点在跑」
	_ = saveIntent(trayIntent{WantNode: true})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.fsm.lastErr = fmt.Sprintf("启动异常: %v", r)
				a.fsm.On(EvStartTimeout)
				a.render()
			}
		}()
		// 起隧道：经 mynetd → VPNDriver.Start（mynetd 已由系统服务保证在跑）
		p, _ := a.prob.Probe()
		if !p.Up {
			a.fsm.lastErr = "本机服务未运行，无法启动隧道"
			a.fsm.On(EvStartTimeout)
			a.render()
			return
		}
		if err := a.startTunnel(); err != nil {
			a.fsm.lastErr = err.Error()
			a.fsm.On(EvStartTimeout)
		} else {
			a.deadline = time.Now().Add(startWait)
		}
		a.render()
	}()
	a.render()
}

func (a *App) onUserStop() {
	if !a.fsm.CanStop() {
		return
	}
	_, act, _ := a.fsm.On(EvStop)
	if act != ActionDoStop {
		a.render()
		return
	}
	// 记住「用户要隧道停着」；**不停 mynetd**
	_ = saveIntent(trayIntent{WantNode: false})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.fsm.lastErr = fmt.Sprintf("停止异常: %v", r)
				a.render()
			}
		}()
		// 只停网口/隧道（VPNDriver.Stop），mynetd 保持常驻
		if err := a.stopTunnel(); err != nil {
			a.fsm.lastErr = err.Error()
		}
		a.deadline = time.Now().Add(stopWait)
		a.render()
	}()
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
	p, err := a.prob.Probe()
	if err != nil {
		// 探针自身故障不该让托盘卡死；按 down 处理
		p = Probe{Up: false}
	}
	ev := eventFromProbe(p)

	// 过渡态超时：探针还没反映结果时，到点就判失败
	if a.fsm.Busy() && !a.deadline.IsZero() && time.Now().After(a.deadline) {
		if a.fsm.State() == StateStarting {
			ev = EvStartTimeout
		} else {
			ev = EvStopTimeout
		}
		a.deadline = time.Time{}
	}

	_, _, changed := a.fsm.On(ev)
	if changed || a.fsm.Busy() {
		a.render()
	} else {
		// 稳态也刷新一次状态行（node-status 的 one_line 会变）
		a.render()
	}
}

func (a *App) render() {
	st := a.fsm.State()
	title := "状态：" + st.Label()
	if a.fsm.LastError() != "" && st == StateFailed {
		title += "（" + a.fsm.LastError() + "）"
	}
	// 只改菜单项与 tooltip，**不** systray.SetTitle（会占菜单栏）
	a.mStatus.SetTitle(title)
	systray.SetTooltip(title)

	if a.fsm.CanStart() {
		a.mStart.Enable()
	} else {
		a.mStart.Disable()
	}
	if a.fsm.CanStop() && !a.fsm.Busy() {
		a.mStop.Enable()
	} else {
		a.mStop.Disable()
	}
}

func (a *App) openPanel() {
	// 根路径是 JSON 壳；面板在 /ui/（embed）或 /gui/（Quasar 控制台）
	url := "http://" + a.base + "/ui/"
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

func (a *App) toggleAutoStart() {
	on, err := a.autoStart.IsEnabled()
	if err != nil {
		return
	}
	if on {
		err = a.autoStart.Disable()
	} else {
		err = a.autoStart.Enable()
	}
	if err != nil {
		return
	}
	a.syncAutoMenu()
}

func (a *App) syncAutoMenu() {
	on, _ := a.autoStart.IsEnabled()
	if on {
		a.mAuto.SetTitle("开机启动 ✓")
	} else {
		a.mAuto.SetTitle("开机启动")
	}
}

// exportConfig / importConfig 走 mynet CLI，托盘不自己打 tar
// （备份逻辑单一来源在 mynet/cmd_config.go，托盘只当壳）。
func (a *App) exportConfig() {
	home, _ := os.UserHomeDir()
	def := filepath.Join(home, fmt.Sprintf("mynet-config-%s.tar.gz", time.Now().Format("20060102-150405")))
	// macOS：osascript 取路径；其他平台先落默认位置
	path := def
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("osascript", "-e",
			`POSIX path of (choose file name with prompt "导出 MyNet 配置" default name "mynet-config.tar.gz")`,
		).Output()
		if err == nil {
			p := strings.TrimSpace(string(out))
			if p != "" {
				path = p
			}
		} else {
			// 用户取消
			return
		}
	}
	if err := runMynet("config", "export", "-o", path); err != nil {
		return
	}
	systray.SetTooltip("已导出 " + path)
}

func (a *App) importConfig() {
	path := ""
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("osascript", "-e",
			`POSIX path of (choose file with prompt "导入 MyNet 配置" of type {"tar.gz", "tgz"})`,
		).Output()
		if err != nil {
			return // 取消
		}
		path = strings.TrimSpace(string(out))
	}
	if path == "" {
		return
	}
	if err := runMynet("config", "import", path); err != nil {
		return
	}
	// 恢复后拉一把**隧道**（经 VPNDriver），不动 mynetd
	_, _, _ = a.fsm.On(EvStart)
	_ = a.startTunnel()
	a.deadline = time.Now().Add(startWait)
	a.render()
}

// runMynet 执行同目录（或 PATH）的 mynet。
func runMynet(args ...string) error {
	bin := "mynet"
	if self := selfPath(); self != "" {
		cand := filepath.Join(filepath.Dir(self), "mynet")
		if runtime.GOOS == "windows" {
			cand += ".exe"
		}
		if _, err := os.Stat(cand); err == nil {
			bin = cand
		}
	}
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		systray.SetTooltip("mynet " + strings.Join(args, " ") + " 失败: " + strings.TrimSpace(string(out)))
		return err
	}
	return nil
}

// AutoStarter 开机自启。
type AutoStarter interface {
	IsEnabled() (bool, error)
	Enable() error
	Disable() error
}

// PanelURL 拼面板地址。
func PanelURL(base string) string {
	return fmt.Sprintf("http://%s/", base)
}

// selfPath 自身可执行文件路径（写自启用）。
func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}
