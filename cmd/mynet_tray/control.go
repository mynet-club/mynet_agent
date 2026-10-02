package main

// 启停 —— 进程生命周期归**平台服务管理器**，不按名字杀进程。
//
//	//	mynetd → launchd club.mynet.mynetd（macOS）
//	//	         systemctl mynetd（Linux）
//	//	         schtasks MyNetd（Windows）
//	//	gnb    → 有 unit 用 unit；无 unit 用 <conf>/gnb.pid 发 SIGTERM
//
// AGENTS.md §3：多实例可同名并存，**禁 pkill / killall / taskkill /im**。
// 「停止异常」多半来自按名字杀失败或误伤 —— 这里只碰**本实例**的 unit 与 pid 文件。

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// Controller 启停本实例（由 MYNET_HOME / 服务标识钉住）。
type Controller interface {
	Start() error
	Stop() error
	Describe() string
}

// NewController 按平台选实现。gnbConf 为本实例 gnb 的 conf 目录（读 gnb.pid）。
func NewController(override string) Controller {
	home := utils.GetMyNetHome()
	switch runtime.GOOS {
	case "darwin":
		return &launchdController{
			label:   "club.mynet.mynetd",
			home:    home,
			gnbConf: defaultGnbConf(home),
		}
	case "windows":
		return &taskController{task: "MyNetd", home: home, gnbConf: defaultGnbConf(home)}
	default:
		return &systemdController{
			unit:     "mynetd",
			gnbUnit:  "mynet_gnb",
			home:     home,
			gnbConf:  defaultGnbConf(home),
			fallback: override,
		}
	}
}

// defaultGnbConf 本实例 gnb 投影目录（node_id 取 mode.json 或扫 conf 下唯一子目录）。
func defaultGnbConf(home string) string {
	// 优先 state/active 记的投影路径；否则 driver/gnb/conf/<id>
	base := filepath.Join(home, "driver", "gnb", "conf")
	ents, err := os.ReadDir(base)
	if err != nil {
		return filepath.Join(home, "var", "gnb") // 旧布局兜底
	}
	for _, e := range ents {
		if e.IsDir() {
			return filepath.Join(base, e.Name())
		}
	}
	return filepath.Join(home, "var", "gnb")
}

// ── macOS: launchd ───────────────────────────────────────────

type launchdController struct {
	label   string
	home    string
	gnbConf string
}

func (c *launchdController) Describe() string { return "launchd/" + c.label }

func (c *launchdController) Start() error {
	// **一次**提权做完：先 bootstrap（未加载时）再 kickstart。
	// 拆成两次 osascript = 弹两次密码。
	script := fmt.Sprintf(
		`launchctl bootstrap system /Library/LaunchDaemons/%s.plist 2>/dev/null || true; launchctl kickstart -k system/%s`,
		c.label, c.label)
	return c.runElevatedShell(script)
}

func (c *launchdController) Stop() error {
	// **一次**提权：bootout 服务；gnb 按 pid 文件（本进程内，无需提权）
	script := fmt.Sprintf(`launchctl bootout system/%s 2>/dev/null || true`, c.label)
	if err := c.runElevatedShell(script); err != nil && !isNotLoaded(err) {
		return fmt.Errorf("launchctl bootout %s: %w（需管理员）", c.label, err)
	}
	return stopGnbByPidFile(c.gnbConf)
}

// runElevatedShell 非 root 时一条 osascript 只弹**一次**密码。
func (c *launchdController) runElevatedShell(shell string) error {
	if os.Geteuid() == 0 {
		return runOK("sh", "-c", shell)
	}
	script := fmt.Sprintf(`do shell script %q with administrator privileges`, shell)
	c2 := exec.Command("osascript", "-e", script)
	setSysProcAttr(c2)
	out, err := c2.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if strings.Contains(msg, "-128") || strings.Contains(msg, "User canceled") || strings.Contains(msg, "User cancelled") {
			return fmt.Errorf("已取消管理员授权")
		}
		return errors.New(msg)
	}
	return nil
}

func shellJoin(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\"'") {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

func isNotLoaded(err error) bool {
	if err == nil {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "No such process") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "No such") ||
		strings.Contains(msg, "service not found") ||
		strings.Contains(msg, "Could not find service")
}

// ── Linux: systemd ───────────────────────────────────────────

type systemdController struct {
	unit     string
	gnbUnit  string
	home     string
	gnbConf  string
	fallback string
}

func (c *systemdController) Describe() string { return "systemd/" + c.unit }

func (c *systemdController) Start() error {
	if err := runOK("systemctl", "start", c.unit); err != nil {
		return fmt.Errorf("systemctl start %s: %w", c.unit, err)
	}
	// gnb unit 存在才 start（mac/容器可能没有）
	_ = runOK("systemctl", "start", c.gnbUnit)
	return nil
}

func (c *systemdController) Stop() error {
	_ = runOK("systemctl", "stop", c.unit)
	_ = runOK("systemctl", "stop", c.gnbUnit)
	return stopGnbByPidFile(c.gnbConf)
}

// ── Windows: 计划任务 ────────────────────────────────────────

type taskController struct {
	task    string
	home    string
	gnbConf string
}

func (c *taskController) Describe() string { return "schtasks/" + c.task }

func (c *taskController) Start() error {
	if err := runOK("schtasks", "/run", "/tn", c.task); err != nil {
		return fmt.Errorf("schtasks /run %s: %w（任务未注册？先跑安装器）", c.task, err)
	}
	return nil
}

func (c *taskController) Stop() error {
	// 只停**隧道**（VPNDriver.Stop），不停 MyNetd 计划任务 —— 与 mac 对齐
	return stopGnbByPidFile(c.gnbConf)
}

// ── gnb：按 pid 文件 SIGTERM（钉 PID，不按名）────────────────

// stopGnbByPidFile 读 <gnbConf>/gnb.pid 并 SIGTERM。
// 与 driver.GNBDriver.Stop 同一约定：gnb -c 时 pid 落在 conf 目录。
func stopGnbByPidFile(confDir string) error {
	if confDir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(confDir, "gnb.pid"))
	if err != nil {
		// 没跑 / 没 pid 文件 = 已停
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读 gnb.pid: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("gnb.pid 非法: %q", strings.TrimSpace(string(raw)))
	}
	// 防自伤：pid 文件陈旧时可能撞上本进程/父进程
	self := os.Getpid()
	if pid == self || pid == os.Getppid() {
		return fmt.Errorf("gnb.pid=%d 指向自身/父进程，拒绝 kill", pid)
	}
	if !pidAlive(pid) {
		_ = os.Remove(filepath.Join(confDir, "gnb.pid"))
		return nil
	}
	// SIGTERM：gnb 装了 handler，优雅退出（不 SIGKILL）
	if err := killPID(pid, "TERM"); err != nil {
		return fmt.Errorf("kill -TERM %d: %w", pid, err)
	}
	return nil
}

func pidAlive(pid int) bool {
	return unixPIDAlive(pid)
}

// runOK 执行命令；失败带 stdout/stderr。不存在的服务等不视作致命时由调用方判。
func runOK(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}
