package main

// 启停 —— 进程生命周期归**平台服务管理器**，不按名字杀进程。
//
//	//	mynetd → launchd com.mynet.mynetd（macOS）
//	//	         systemctl mynetd（Linux）
//	//	         SCM 服务 mynetd（Windows）
//	//	gnb    → 有 unit 用 unit；无 unit 用 <conf>/gnb.pid 发 SIGTERM
//
// ⚠️ 启停目标 = install 的**注册名**（lifecycle-contract §3 服务名矩阵），
// 标识收敛在 shared/utils/service_names.go。此前两侧各写各的字面量，
// 托盘打的是 install 从不注册的标识（club.mynet.mynetd / schtasks MyNetd），
// 启停等于摆设（C1-S3）。
//
// AGENTS.md §3：多实例可同名并存，**禁 pkill / killall / taskkill /im**。
// 「停止异常」多半来自按名字杀失败或误伤 —— 这里只碰**本实例**的 unit 与 pid 文件。

import (
	"encoding/json"
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
	return newController(runtime.GOOS, override)
}

// newController 平台入口与实现分离，测试可对每个 GOOS 断言
// 「托盘启停目标 == install 注册名」（见 control_test.go）。
func newController(goos, override string) Controller {
	home := utils.GetMyNetHome()
	switch goos {
	case "darwin":
		return &launchdController{
			label:   utils.LaunchdLabelMynetd,
			home:    home,
			gnbConf: defaultGnbConf(home),
		}
	case "windows":
		return &scmController{
			service: utils.ServiceNameMynetd,
			home:    home,
			gnbConf: defaultGnbConf(home),
		}
	default:
		return &systemdController{
			unit:     utils.ServiceNameMynetd,
			gnbUnit:  utils.ServiceNameGNB,
			home:     home,
			gnbConf:  defaultGnbConf(home),
			fallback: override,
		}
	}
}

// defaultGnbConf 本实例 gnb conf 目录（D4-E：节点唯一存放位 conf/<form>/<nodeId>/；
// 旧 driver/gnb/conf 与 var/gnb 只作回退）。
func defaultGnbConf(home string) string {
	// 优先 mode.json 的 form+node
	form, nodeID := "local", ""
	if raw, err := os.ReadFile(filepath.Join(home, "conf", "mode.json")); err == nil {
		var m struct {
			Mode   string `json:"mode"`
			Active struct {
				NodeID string `json:"node_id"`
			} `json:"active"`
		}
		if json.Unmarshal(raw, &m) == nil {
			if m.Mode == "online" {
				form = "online"
			}
			nodeID = m.Active.NodeID
		}
	}
	if nodeID != "" {
		p := filepath.Join(home, "conf", form, nodeID)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	// 旧投影树
	base := filepath.Join(home, "driver", "gnb", "conf")
	ents, err := os.ReadDir(base)
	if err == nil {
		for _, e := range ents {
			if e.IsDir() {
				return filepath.Join(base, e.Name())
			}
		}
	}
	return filepath.Join(home, "var", "gnb") // 更旧布局兜底
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

// ── Windows: SCM（install 注册的唯一常驻机制，lifecycle-contract §3）──
//
// 旧实现打 `schtasks /run MyNetd` —— install 早已改 SCM 注册 mynetd 并把
// schtasks mynetd/mynet-gnb 当旧物删除（cmd_install_win.go），托盘启停
// 打在一个从不注册的计划任务上，按钮是摆设（C1-S3 / CPI-73·74）。

type scmController struct {
	service string
	home    string
	gnbConf string
}

func (c *scmController) Describe() string { return "scm/" + c.service }

func (c *scmController) Start() error {
	if err := runOK("sc.exe", "start", c.service); err != nil {
		return fmt.Errorf("sc start %s: %w（服务未注册？先跑 mynet install）", c.service, err)
	}
	return nil
}

func (c *scmController) Stop() error {
	// 与 mac/linux 一致：停托管服务 + 按 pid 文件停 gnb
	if err := runOK("sc.exe", "stop", c.service); err != nil && !isNotLoaded(err) {
		return fmt.Errorf("sc stop %s: %w（需管理员）", c.service, err)
	}
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
