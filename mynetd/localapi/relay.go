package localapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent-V2/gateway/relaypolicy"
	"github.com/mynet-club/mynet_agent-V2/shared/utils"
)

// /api/v1/relay —— GUI → mynetd → gateway → back 的**两跳**中继。
//
// 与 gateway 自己的 /v1/relay 不同（那个是 console/移动端直连）：
// 本机 webui 的信任边界止于 127.0.0.1，浏览器**不直接**出门。
// mynetd 持有 gateway 地址（MYNET_GATEWAYS），浏览器只跟本机说话。
//
// 契约：`ANY /api/v1/relay?path=<back-api-path>`，method/body/头原样透传，
// 响应原样回写。鉴权头（Authorization）一并转给 back —— gateway 只做
// 路径白名单与内网转发，不碰鉴权（relaypolicy B3：纯路径，无鉴权语义）。
//
// ⚠️ 路径白名单在**这一跳也要查**：不能假设上游一定挡得住。
// 复用 relaypolicy.IsAllowed（先 deny 后 allow，含 % 一律拒）。
func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.Query().Get("path")
	if rawPath == "" {
		writeJSON(w, 400, map[string]any{"success": false, "message": "缺少 path 参数：/api/v1/relay?path=<back-api-path>"})
		return
	}
	if !relaypolicy.IsAllowed(rawPath) {
		writeJSON(w, 403, map[string]any{"success": false, "message": "path 不在中继白名单内；鉴权归 back"})
		return
	}

	gw := firstGateway()
	if gw == "" {
		writeJSON(w, 502, map[string]any{
			"success": false,
			"message": "未配置 MYNET_GATEWAYS —— 平台面需要 gateway 中继",
		})
		return
	}

	target, err := url.Parse(strings.TrimRight(gw, "/") + "/v1/relay?path=" + url.QueryEscape(rawPath))
	if err != nil {
		writeJSON(w, 502, map[string]any{"success": false, "message": "gateway 地址非法: " + err.Error()})
		return
	}

	// 重建请求：body 要能重读，方法与头（含 Authorization）原样
	var body io.Reader
	if r.Body != nil {
		body = r.Body
	}
	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), body)
	if err != nil {
		writeJSON(w, 502, map[string]any{"success": false, "message": err.Error()})
		return
	}
	// 透传会话头；不透传 Host/Content-Length（由 client 重算）
	for k, vs := range r.Header {
		lk := strings.ToLower(k)
		if lk == "host" || lk == "content-length" || lk == "connection" {
			continue
		}
		for _, v := range vs {
			proxyReq.Header.Add(k, v)
		}
	}
	if proxyReq.Header.Get("Accept") == "" {
		proxyReq.Header.Set("Accept", "application/json")
	}

	client := &http.Client{Timeout: relayTimeout}
	resp, err := client.Do(proxyReq)
	if err != nil {
		writeJSON(w, 502, map[string]any{"success": false, "message": "gateway relay 失败: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		if strings.ToLower(k) == "content-length" {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

const relayTimeout = 30 * time.Second // 与 gateway 侧一致

func firstGateway() string {
	gw := os.Getenv("MYNET_GATEWAYS")
	if gw == "" {
		// launchd/桌面安装不一定注入 MYNET_GATEWAYS：回退 agent.json 里**显式**的 gateway。
		// 不用 LoadAgentCfg 的代码默认值 —— 那是 WS 拨号默认；relay 要的是「本机确实配了网关」。
		gw = agentGatewayExplicit()
	}
	if gw == "" {
		return ""
	}
	gw = strings.Split(gw, ",")[0]
	// MYNET_GATEWAYS / agent.json 是 WS 入口（wss://…）；relay 是同主机的 HTTPS 面。
	// 不换协议会得到 `unsupported protocol scheme "wss"` —— 实测撞过。
	if strings.HasPrefix(gw, "wss://") {
		return "https://" + strings.TrimPrefix(gw, "wss://")
	}
	if strings.HasPrefix(gw, "ws://") {
		return "http://" + strings.TrimPrefix(gw, "ws://")
	}
	return gw
}

// agentGatewayExplicit 只读 agent.json 文件里写明的 gateway，不读代码默认。
func agentGatewayExplicit() string {
	b, err := os.ReadFile(utils.AgentConfigPath())
	if err != nil {
		return ""
	}
	var j struct {
		Gateway string `json:"gateway"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return ""
	}
	return strings.TrimSpace(j.Gateway)
}
