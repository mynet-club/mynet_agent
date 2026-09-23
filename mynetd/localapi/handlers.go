package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent-V2/shared"
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

func allowedSubcommand(cmd string) bool {
	switch cmd {
	case "detector", "metrics", "apply", "service", "driver",
		"setup", "install", "uninstall", "update", "config",
		"status", "monitor", "check", "nodes", "platform":
		return true
	}
	return false
}

func (s *Server) dispatch(w http.ResponseWriter, argv []string) {
	if s.Ops == nil {
		// 默认：exec mynet <argv>（桌面/Windows）；OpenWrt 装配时换成 lua
		s.Ops = defaultExecMynet
	}
	out, err := s.Ops(argv)
	if err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": out})
}

func defaultExecMynet(argv []string) (any, error) {
	// 执行器：桌面 exec mynet <子命令>；OpenWrt 装配时换 lua
	bin := os.Getenv("MYNET_BIN")
	if bin == "" {
		bin = "mynet"
	}
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

func systemJSONPath() string {
	return filepath.Join(os.Getenv("MYNET_HOME"), "var", "system.json")
}
