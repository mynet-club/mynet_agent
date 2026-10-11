package localapi

import (
	"net/http"
	"strings"
)

// Serialize context/key mutations with connect, including across browser tabs.
// Reads stay available while an operation is running.
func (s *Server) withConfigMutation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next(w, r)
			return
		}
		if !s.configMutation.TryLock() {
			w.Header().Set("Retry-After", "2")
			writeJSON(w, http.StatusConflict, map[string]any{"success": false, "code": "operation_in_progress", "message": "已有配置操作正在执行，请稍后刷新状态再试", "retryable": true})
			return
		}
		defer s.configMutation.Unlock()
		next(w, r)
	}
}

// TryRunExclusive 以 configMutation 执行 fn（进程内写面，如启动时的隧道恢复）。
//
// 与 HTTP 写面互斥：拿到锁则 fn 独占地写节点状态（此间 HTTP 写请求 409），
// 拿不到（有用户写在进行）则**跳过**并返回 false —— 启动恢复是尽力而为，
// 用户正在写的时候以用户为准，不阻塞、不排队。
func (s *Server) TryRunExclusive(fn func() error) bool {
	if !s.configMutation.TryLock() {
		return false
	}
	defer s.configMutation.Unlock()
	_ = fn()
	return true
}

// One connect request owns the sequence; every system operation stays in Ops.
// The response records completed steps and the failure stage, never key data.
func (s *Server) handleConfigConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "POST only"})
		return
	}
	steps := []map[string]any{}
	reply := func(status int, stage, message string) {
		payload := map[string]any{"success": status == 200, "stage": stage, "message": redactSecrets(message), "steps": steps, "retryable": status != 200 && status != 400}
		if strings.Contains(message, "need_login") {
			status = 401
			payload["code"] = "need_login"
			payload["need_login"] = true
			payload["message"] = "登录已失效，请重新登录后重试"
		}
		writeJSON(w, status, payload)
	}
	m, err := s.opsJSON([]string{"config", "status", "--json"})
	if err != nil {
		reply(502, "identity", "读取设备身份失败: "+err.Error())
		return
	}
	active, _ := m["active"].(map[string]any)
	driver := driverFromConfigResult(m)
	nodeID, _ := active["node_id"].(string)
	if m["initialized"] != true || m["mode"] != "online" || nodeID == "" || driver == "" {
		reply(400, "identity", "请先登录并选择云端设备，再接入本机")
		return
	}
	for _, step := range []struct {
		id, name string
		argv     []string
	}{
		{"keys", "准备本机身份", []string{"config", "keys", "ensure", "--json"}},
		{"sync", "同步公钥与启用设备", []string{"config", "keys", "sync", "--connect", "--json"}},
		{"pull", "加载网络配置", []string{"config", "pull", "--download-only", "--json"}},
	} {
		state := map[string]any{"id": step.id, "name": step.name, "state": "running"}
		steps = append(steps, state)
		if _, err := s.opsJSON(step.argv); err != nil {
			state["state"] = "failed"
			reply(502, step.id, step.name+"失败: "+err.Error())
			return
		}
		state["state"] = "done"
	}
	for _, step := range []struct {
		id, name string
		argv     []string
	}{
		{"apply", "生成运行配置", []string{"apply", "--active"}},
		{"start", "启动网络连接", []string{"service", "start", driver}},
		{"routes", "应用系统路由", []string{"apply", "--auto"}},
	} {
		state := map[string]any{"id": step.id, "name": step.name, "state": "running"}
		steps = append(steps, state)
		if out, err := s.Ops(step.argv); err != nil {
			state["state"] = "failed"
			// A partially started tunnel must not survive a failed connect as a success.
			if step.id == "start" || step.id == "routes" {
				cleanup := map[string]any{"id": "cleanup", "name": "停止未完成的连接", "state": "done"}
				if _, stopErr := s.Ops([]string{"service", "stop", driver}); stopErr != nil {
					cleanup["state"] = "failed"
				}
				steps = append(steps, cleanup)
				markTunnelWantDriver(false, driver)
			}
			message := err.Error()
			if data, ok := out.(map[string]any); ok {
				if detail, ok := data["stderr"].(string); ok && strings.TrimSpace(detail) != "" {
					message = strings.TrimSpace(detail)
				}
			}
			reply(502, step.id, step.name+"失败: "+message)
			return
		}
		state["state"] = "done"
	}
	markTunnelWantDriver(true, driver)
	reply(200, "started", "连接已启动，正在检查实际状态")
}

func isContextMutation(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch argv[0] {
	case "service", "setup", "apply", "install", "uninstall", "update", "refresh-config", "refresh-routes", "refresh-address", "plugin":
		return true
	case "node":
		return len(argv) > 1 && argv[1] == "claim"
	case "config":
		if len(argv) < 2 {
			return false
		}
		switch argv[1] {
		case "status", "identity", "runtime", "cloud", "plugins", "tenants", "parity":
			return false
		case "keys":
			return len(argv) > 2 && argv[2] != "show" && argv[2] != "status" && argv[2] != "--json"
		default:
			return true
		}
	}
	return false
}

func getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, 405, map[string]any{"success": false, "message": "GET only"})
			return
		}
		next(w, r)
	}
}
