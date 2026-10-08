package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/utils"
	"time"

	"github.com/mynet-club/mynet_agent/shared"
)

// handleSystemJSON — mynetd 本机态直读 system.json（不 fork，AGENTS.md §1.1）
func (s *Server) handleSystemJSON(w http.ResponseWriter, r *http.Request) {
	snap, err := shared.ReadSnapshot(shared.DefaultSnapshotPath())
	if err != nil {
		writeJSON(w, 404, map[string]any{"success": false, "message": "system.json 不存在，请先 mynet detector"})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": snap})
}

// handleMetrics — 委托执行器 `mynet metrics --json`（心跳数据源）
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.dispatch(w, []string{"metrics", "--json"})
}

// handleCommand — 通用分派：POST {"argv":["detector","--json"]}
func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "POST only"})
		return
	}
	var body struct {
		Argv []string `json:"argv"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Argv) == 0 {
		writeJSON(w, 400, map[string]any{"success": false, "message": "argv required"})
		return
	}
	// 白名单：只允许 mynet 子命令面，不允许任意路径
	if !allowedSubcommand(body.Argv[0]) {
		writeJSON(w, 400, map[string]any{"success": false, "message": "subcommand not allowed"})
		return
	}
	// 启停意图：供 mynetd 开机恢复隧道（agent.json 开关）
	if len(body.Argv) >= 2 && body.Argv[0] == "service" {
		driver := "gnb"
		if len(body.Argv) >= 3 && body.Argv[2] == "wireguard" {
			driver = "wireguard"
		}
		switch body.Argv[1] {
		case "start":
			markTunnelWantDriver(true, driver)
		case "stop":
			markTunnelWantDriver(false, driver)
		}
	}
	s.dispatch(w, body.Argv)
}

// handleCommandResult E-4：Lua/mynet 执行结果回 mynetd（不再直连 back）。
func (s *Server) handleCommandResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "POST only"})
		return
	}
	var body struct {
		ID      string `json:"id"`
		OK      bool   `json:"ok"`
		Message string `json:"message"`
		Source  string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeJSON(w, 400, map[string]any{"success": false, "message": "id required"})
		return
	}
	if s.OnResult != nil {
		s.OnResult(body.ID, body.OK, body.Message, body.Source)
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": body})
}

// allowedSubcommand 只允许 mynet 子命令面，不允许任意路径。
//
// ⚠️ `auth`/`tenants`/`node` 是 2026-09-25 为「首次运行向导」加的
// （向导三步：登录 → 选节点 → 下载配置）。它们**不含秘密**：
// 口令不走 argv（会进 `ps`），而走专用的 /api/v1/wizard/login 用 stdin 喂
// `mynet auth login --password-stdin`。所以子命令名本身可以白名单放行。
func allowedSubcommand(cmd string) bool {
	switch cmd {
	case "detector", "metrics", "apply", "service", "driver",
		"setup", "install", "uninstall", "update", "config",
		"refresh-config", "refresh-routes", "refresh-address",
		"status", "monitor", "check", "nodes", "platform", "plugin",
		// 向导用到的账号态/节点归属原语
		"auth", "tenants", "node":
		return true
	}
	return false
}

func (s *Server) dispatch(w http.ResponseWriter, argv []string) {
	if isContextMutation(argv) {
		if !s.configMutation.TryLock() {
			w.Header().Set("Retry-After", "2")
			writeJSON(w, 409, map[string]any{"success": false, "code": "operation_in_progress", "message": "已有配置操作正在执行，请稍后刷新状态再试", "retryable": true})
			return
		}
		defer s.configMutation.Unlock()
	}

	if s.Ops == nil {
		// 默认：exec mynet <argv>（桌面/Windows）；OpenWrt 装配时换成 lua
		s.Ops = defaultExecMynet
	}
	out, err := s.Ops(argv)
	if err != nil {
		// ⚠️ 必须把 stdout/stderr 一起回，不能只回 err.Error()。
		// `exit status 1` 说明不了任何事 —— 真正的原因在子进程的 stderr 里，
		// 而 defaultExecMynet 已经把它放进 out 了，扔掉等于让每次失败都
		// 变成「exit status 1」，只能靠人再手动跑一遍命令去猜。
		payload := map[string]any{"success": false, "message": err.Error()}
		if m, ok := out.(map[string]any); ok {
			if v, ok := m["stdout"]; ok {
				payload["stdout"] = sanitizeValue(v)
			}
			if v, ok := m["stderr"]; ok {
				payload["stderr"] = sanitizeValue(v)
			}
			if v, ok := m["argv"]; ok {
				payload["argv"] = v
			}
		}
		writeJSON(w, 500, payload)
		return
	}
	// 统一脱敏：/api/v1/command 的输出与向导端点一样直接进浏览器
	// （webui 的 cmd-out 就是个 <pre>）。之前只在 wizard 端点脱敏，
	// 于是走这条的 `auth status` 之类照样能把 token 前缀吐到 UI 上。
	// 规则见 redactSecrets：PAT 形状无条件遮，长 hex 只在带 secret 关键字时遮，
	// **公钥不误杀**（它是给人核对的）。
	if m, ok := out.(map[string]any); ok {
		out = sanitizeWizardOut(m)
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": out})
}

func defaultExecMynet(argv []string) (any, error) {
	// 执行器：桌面 exec mynet <子命令>；OpenWrt 装配时换 lua
	bin := resolveMynetBin()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := map[string]any{
		"argv":   argv,
		"stdout": stdout.String(),
		"stderr": stderr.String(),
		"bin":    bin,
	}
	if err != nil {
		out["error"] = err.Error()
		return out, err
	}
	return out, nil
}

// resolveMynetBin 找执行器二进制。
//
// 顺序：MYNET_BIN → mynetd 同目录 → PATH。
// 为什么要看同目录：Windows 上装完是 `C:\mynet\bin\{mynetd,mynet}.exe`，
// 这个目录通常**不在 PATH** 里。只查 PATH 会让 mynetd 一起来就
// 「找不到 mynet」，每个分派都 500 —— 真机实测咬过（V5-2）。
// 同目录查找对 linux/darwin 的 /opt/mynet/bin 形态同样有用。
func resolveMynetBin() string {
	if bin := os.Getenv("MYNET_BIN"); bin != "" {
		return bin
	}
	name := utils.ExeName("mynet")
	if self, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(self), name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	// PATH 查找；LookPath 在 Windows 上会自动补 .exe
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if p, err := exec.LookPath("mynet"); err == nil {
		return p
	}
	return name // 让 Run() 报出带名字的错误，而不是空路径
}

// sanitizeValue 对非 map 的值也走一遍脱敏（错误分支里 stdout/stderr 是字符串）。
func sanitizeValue(v any) any {
	if str, ok := v.(string); ok {
		return redactSecrets(str)
	}

	return v
}

func systemJSONPath() string {
	return filepath.Join(os.Getenv("MYNET_HOME"), "var", "system.json")
}
