package localapi

// 本地面板配置面：HTTP 壳。配置读写/密钥/切换全在执行器 `mynet config`。
//
// 边界（AGENTS.md §1.2 mynetd 纯通信）：
//   - 这里只做路由、请求解析、调 Ops、套 {success,data}
//   - 不 import shared/config · mynet/keys —— 不碰配置文件格式
//   - 切换后经 Ops 跑 `apply --active` 重生投影
//
// 执行器契约：`mynet config … --json` 在 stdout 吐**一个** JSON 对象。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

func (s *Server) registerConfigRoutes() {
	s.mux.HandleFunc("/api/v1/config/mode", s.withConfigMutation(s.handleConfigMode))
	s.mux.HandleFunc("/api/v1/config/tenants", getOnly(s.handleConfigTenants))
	s.mux.HandleFunc("/api/v1/config/active", s.withConfigMutation(s.handleConfigActive))
	s.mux.HandleFunc("/api/v1/config/keys", s.withConfigMutation(s.handleConfigKeys))
	s.mux.HandleFunc("/api/v1/config/identity", getOnly(s.handleConfigIdentity))
	s.mux.HandleFunc("/api/v1/config/runtime", getOnly(s.handleConfigRuntime))
	s.mux.HandleFunc("/api/v1/config/cloud", getOnly(s.handleConfigCloud))
	s.mux.HandleFunc("/api/v1/config/connect", s.withConfigMutation(s.handleConfigConnect))
	s.mux.HandleFunc("/api/v1/config/prepare", s.withConfigMutation(s.handleConfigPrepare))
	s.mux.HandleFunc("/api/v1/plugins", getOnly(s.handlePlugins))
	s.mux.HandleFunc("/api/v1/config/apply", s.withConfigMutation(s.handleConfigApply))
}

// GET /api/v1/config/cloud — 账号下完整的云端设备清单。
// 分页与 token 续期都由执行器处理；mynetd 只转发结构化结果。
func (s *Server) handleConfigCloud(w http.ResponseWriter, r *http.Request) {
	data, err := s.opsJSON([]string{"config", "cloud", "--json"})
	if err != nil {
		status := http.StatusBadGateway
		if strings.Contains(err.Error(), "need_login") {
			status = http.StatusUnauthorized
		}
		writeJSON(w, status, map[string]any{"success": false, "message": err.Error(), "need_login": status == http.StatusUnauthorized})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": data})
}

// GET /api/v1/config/identity — 空间名/用户名/节点名
func (s *Server) handleConfigIdentity(w http.ResponseWriter, r *http.Request) {
	data, err := s.opsJSON([]string{"config", "identity", "--json"})
	if err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": data})
}

// GET /api/v1/config/runtime — node/route/address 原文 + 对端/网段计数
func (s *Server) handleConfigRuntime(w http.ResponseWriter, r *http.Request) {
	data, err := s.opsJSON([]string{"config", "runtime", "--json"})
	if err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": data})
}

// opsJSON 调执行器并解开 stdout 的 JSON 对象。
func (s *Server) opsJSON(argv []string) (map[string]any, error) {
	if s.Ops == nil {
		s.Ops = defaultExecMynet
	}
	out, err := s.Ops(argv)
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("执行器返回非对象")
	}
	if e, ok := m["error"]; ok && e != nil && e != "" {
		// 执行器 JSON 里的业务错误（如 {"error":"..."}）
		if es, ok := e.(string); ok && es != "" {
			return nil, fmt.Errorf("%s", es)
		}
	}
	stdout, _ := m["stdout"].(string)
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		if errv, ok := m["error"]; ok {
			return nil, fmt.Errorf("%v", errv)
		}
		return nil, fmt.Errorf("执行器无输出")
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(stdout), &data); err != nil {
		// 数组等形态由调用方处理；这里只接受对象
		return nil, fmt.Errorf("执行器输出非 JSON 对象: %w", err)
	}
	if e, ok := data["error"].(string); ok && e != "" {
		return nil, fmt.Errorf("%s", e)
	}
	return data, nil
}

// opsJSONRaw 同 opsJSON，但允许数组（tenants/plugins 列表）。
func (s *Server) opsJSONRaw(argv []string) (any, error) {
	if s.Ops == nil {
		s.Ops = defaultExecMynet
	}
	out, err := s.Ops(argv)
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("执行器返回非对象")
	}
	stdout, _ := m["stdout"].(string)
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		if errv, ok := m["error"]; ok {
			return nil, fmt.Errorf("%v", errv)
		}
		return nil, fmt.Errorf("执行器无输出")
	}
	var data any
	if err := json.Unmarshal([]byte(stdout), &data); err != nil {
		return nil, fmt.Errorf("执行器输出非 JSON: %w", err)
	}
	return data, nil
}

// GET/POST /api/v1/config/keys —— 节点密钥（恢复通信）。
// 薄壳：读写都在执行器 `mynet config keys … --json`（B2 不碰配置文件）。
func (s *Server) handleConfigKeys(w http.ResponseWriter, r *http.Request) {
	// 拉取/生成前先看登录态：未登录时拉的配置是错的
	if r.Method == http.MethodGet {
		// GET keys 不要求登录（本地模式也要看密钥）
	} else {
		// 上传公钥/claim 需要登录 —— 由上层 wizard 控制
	}
	if r.Method == http.MethodGet {
		data, err := s.opsJSON([]string{"config", "keys", "show", "--json"})
		if err != nil {
			writeJSON(w, 500, map[string]any{"success": false, "message": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "data": data})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "GET|POST only"})
		return
	}
	var body struct {
		Generate   bool   `json:"generate"`
		PrivateHex string `json:"private_hex"`
		PublicHex  string `json:"public_hex"`
		NodeID     string `json:"node_id"`
		VPNType    string `json:"vpn_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"success": false, "message": "bad json"})
		return
	}

	var argv []string
	switch {
	case body.Generate:
		argv = []string{"config", "keys", "generate", "--json"}
		if body.VPNType != "" {
			argv = append(argv, "--vpn", body.VPNType)
		}
		if body.NodeID != "" {
			argv = append(argv, "--node-id", body.NodeID)
		}
	case body.PrivateHex != "":
		// 公钥可省略 —— 执行器从私钥推导并做配对校验（私钥是身份源）
		argv = []string{"config", "keys", "set", "--json", "--private", body.PrivateHex}
		if body.PublicHex != "" {
			argv = append(argv, "--public", body.PublicHex)
		}
		if body.NodeID != "" {
			argv = append(argv, "--node-id", body.NodeID)
		}
	default:
		writeJSON(w, 400, map[string]any{"success": false, "message": "需要 generate=true 或 private_hex（公钥由私钥自动推导）"})
		return
	}

	data, err := s.opsJSON(argv)
	if err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": data})
}

// /api/v1/config/mode — GET 读激活态；POST 切换 local/online。
func (s *Server) handleConfigMode(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		data, err := s.opsJSON([]string{"config", "status", "--json"})
		if err != nil {
			writeJSON(w, 500, map[string]any{"success": false, "message": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "data": data})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "GET|POST only"})
		return
	}
	var body struct {
		Mode   string `json:"mode"`
		Tenant string `json:"tenant"`
		Node   string `json:"node"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"success": false, "message": "bad json"})
		return
	}

	var argv []string
	switch body.Mode {
	case "local":
		argv = []string{"config", "use", "local", "--json"}
	case "online":
		if body.Tenant == "" {
			writeJSON(w, 400, map[string]any{"success": false, "message": "online 需要 tenant"})
			return
		}
		argv = []string{"config", "use", "online", "--json", "--tenant", body.Tenant}
		if body.Node != "" {
			argv = append(argv, "--node", body.Node)
		}
	default:
		writeJSON(w, 400, map[string]any{"success": false, "message": "mode 必须是 local|online"})
		return
	}

	s.switchConfig(w, argv)
}

// GET /api/v1/config/tenants
func (s *Server) handleConfigTenants(w http.ResponseWriter, r *http.Request) {
	data, err := s.opsJSONRaw([]string{"config", "tenants", "--json"})
	if err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": data})
}

// POST /api/v1/config/active {"tenant":"t_1","node":"n1"}  同租户切节点
//
// setup_only=true —— 配置向导第②步「选节点」：只落 profile + mode.json
// （setup --online 的语义），**不**停旧驱动 / apply / start。此刻本机往往还没有
// 密钥与 bundle，走全量切换会停在 apply 阶段报缺密钥 —— 那不是失败，是流程没走完。
func (s *Server) handleConfigActive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "POST only"})
		return
	}
	var body struct {
		Tenant    string `json:"tenant"`
		Node      string `json:"node"`
		SetupOnly bool   `json:"setup_only"`
		VPNType   string `json:"vpn_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]any{"success": false, "message": "bad json"})
		return
	}
	if body.Tenant == "" {
		writeJSON(w, 400, map[string]any{"success": false, "message": "需要 tenant"})
		return
	}
	if body.SetupOnly {
		if body.Node == "" {
			writeJSON(w, 400, map[string]any{"success": false, "message": "setup_only 需要 node"})
			return
		}
		argv := []string{"setup", "--online", "--tenant", body.Tenant, "--node-id", body.Node}
		if body.VPNType != "" {
			if body.VPNType != "gnb" && body.VPNType != "wireguard" {
				writeJSON(w, 400, map[string]any{"success": false, "message": "不支持的 VPN 类型"})
				return
			}
			argv = append(argv, "--vpn-type", body.VPNType)
		}
		out, err := s.execMynetStd(argv)
		if err != nil {
			writeJSON(w, 400, map[string]any{"success": false, "stage": "setup",
				"message": "选节点失败: " + err.Error(), "data": sanitizeWizardOut(out)})
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "data": sanitizeWizardOut(out)})
		return
	}
	argv := []string{"config", "use", "online", "--json", "--tenant", body.Tenant}
	if body.Node != "" {
		argv = append(argv, "--node", body.Node)
	}
	s.switchConfig(w, argv)
}

// switchConfig 完成一次真正的运行上下文切换：停旧驱动 → 改激活配置 →
// 生成目标驱动投影 → 启动目标驱动 → 安装 OS 路由。浏览器只调用本地 URL，
// 每一步都经 Ops 落到 mynet command。
func (s *Server) switchConfig(w http.ResponseWriter, useArgv []string) {
	oldDriver := s.activeDriver()
	if oldDriver != "" {
		_, _ = s.Ops([]string{"service", "stop", oldDriver})
	}
	m, err := s.opsJSON(useArgv)
	if err != nil {
		writeJSON(w, 400, map[string]any{"success": false, "message": err.Error(), "stage": "switch"})
		return
	}
	if _, err := s.Ops([]string{"apply", "--active"}); err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "data": m, "message": "配置已切换，但生成运行配置失败: " + err.Error(), "stage": "apply"})
		return
	}
	newDriver := driverFromConfigResult(m)
	if newDriver == "" {
		newDriver = s.activeDriver()
	}
	if newDriver == "" {
		newDriver = "gnb"
	}
	if _, err := s.Ops([]string{"service", "start", newDriver}); err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "data": m, "message": "运行配置已生成，但启动失败: " + err.Error(), "stage": "start", "driver": newDriver})
		return
	}
	markTunnelWantDriver(true, newDriver)
	if _, err := s.Ops([]string{"apply", "--auto"}); err != nil {
		writeJSON(w, 200, map[string]any{"success": true, "data": m, "warning": "节点已启动，但系统路由未完全应用: " + err.Error(), "stage": "routes", "driver": newDriver})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": m, "stage": "running", "driver": newDriver})
}

func driverFromConfigResult(m map[string]any) string {
	if active, ok := m["active"].(map[string]any); ok {
		if d, _ := active["driver"].(string); d == "gnb" || d == "wireguard" {
			return d
		}
	}
	if d, _ := m["driver"].(string); d == "gnb" || d == "wireguard" {
		return d
	}
	return ""
}

func (s *Server) activeDriver() string {
	m, err := s.opsJSON([]string{"config", "status", "--json"})
	if err != nil || m["initialized"] != true {
		return ""
	}
	return driverFromConfigResult(m)
}

// GET /api/v1/plugins
func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	data, err := s.opsJSONRaw([]string{"config", "plugins", "--json"})
	if err != nil {
		writeJSON(w, 500, map[string]any{"success": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": data})
}

// POST /api/v1/config/apply
// apply 只写 driver 投影；随后 **service start** 真正拉起隧道。
func (s *Server) handleConfigApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "POST only"})
		return
	}
	if s.Ops == nil {
		writeJSON(w, 500, map[string]any{"success": false, "message": "Ops 未注入"})
		return
	}
	var body struct {
		Start *bool `json:"start"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	start := true
	if body.Start != nil {
		start = *body.Start
	}

	steps := []map[string]any{{"name": "生成运行配置", "state": "running"}}
	out, err := s.Ops([]string{"apply", "--active"})
	if err != nil {
		msg := err.Error()
		if m, ok := out.(map[string]any); ok {
			if se, _ := m["stderr"].(string); strings.TrimSpace(se) != "" {
				msg = strings.TrimSpace(se)
			}
		}
		steps[0]["state"] = "failed"
		writeJSON(w, 500, map[string]any{"success": false, "message": msg, "steps": steps})
		return
	}
	steps[0]["state"] = "done"
	started := ""
	if start {
		steps = append(steps, map[string]any{"name": "重启网络连接", "state": "running"})
		driver := s.activeDriver()
		if driver == "" {
			driver = "gnb"
		}
		// apply 投影 → service start 隧道 → apply --auto 装 OS 路由
		if so, err := s.Ops([]string{"service", "start", driver}); err != nil {
			steps[len(steps)-1]["state"] = "failed"
			writeJSON(w, 500, map[string]any{
				"success": false, "data": out, "steps": steps,
				"message": "运行配置已生成，但网络连接启动失败: " + err.Error(),
			})
			return
		} else {
			started = fmt.Sprintf("%v", so)
			steps[len(steps)-1]["state"] = "done"
			markTunnelWantDriver(true, driver)
		}
		steps = append(steps, map[string]any{"name": "应用系统路由和防火墙", "state": "running"})
		if _, err := s.Ops([]string{"apply", "--auto"}); err != nil {
			steps[len(steps)-1]["state"] = "failed"
			writeJSON(w, 500, map[string]any{
				"success": false, "data": out, "started": started, "steps": steps,
				"message": "网络已启动，但系统路由或防火墙应用失败: " + err.Error(),
			})
			return
		}
		steps[len(steps)-1]["state"] = "done"
	}
	writeJSON(w, 200, map[string]any{"success": true, "data": out, "started": started, "steps": steps})
}

// markTunnelWant 记录用户意图（启动/停止），供 mynetd 开机恢复。
func markTunnelWant(on bool) {
	markTunnelWantDriver(on, "gnb")
}

func markTunnelWantDriver(on bool, driver string) {
	if driver != "wireguard" {
		driver = "gnb"
	}
	path := filepath.Join(utils.VarDir(), "tunnel-state.json")
	b, _ := json.MarshalIndent(map[string]any{
		"want_running": on,
		"driver":       driver,
		"updated_at":   time.Now().UTC().Format(time.RFC3339),
	}, "", "  ")
	_ = os.WriteFile(path, append(b, '\n'), 0o600)
}

// prepare is a thin structured wrapper: key sync and pull stay in the executor.
// Unlike the generic command endpoint, it checks JSON business errors too.
func (s *Server) handleConfigPrepare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"success": false, "message": "POST only"})
		return
	}
	for _, step := range []struct {
		name string
		argv []string
	}{
		{"keys", []string{"config", "keys", "ensure", "--json"}},
		{"sync", []string{"config", "keys", "sync", "--connect", "--json"}},
		{"pull", []string{"config", "pull", "--download-only", "--json"}},
	} {
		if _, err := s.opsJSON(step.argv); err != nil {
			writeJSON(w, 502, map[string]any{"success": false, "stage": step.name, "message": err.Error()})
			return
		}
	}
	writeJSON(w, 200, map[string]any{"success": true})
}
