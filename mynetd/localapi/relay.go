package localapi

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/gateway/relaypolicy"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// /api/v1/relay —— GUI → mynetd → gateway → 后端的**两跳**中继。
//
// 与 gateway 自己的 /v1/relay 不同（那个是 console/移动端直连）：
// 本机 webui 的信任边界止于 127.0.0.1，浏览器**不直接**出门。
// mynetd 持有 gateway 地址（MYNET_GATEWAYS），浏览器只跟本机说话。
//
// 契约：`ANY /api/v1/relay?path=<back-api-path>`，method/body/头原样透传，
// 响应原样回写。鉴权头（Authorization）一并转给后端—— gateway 只做
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
		writeJSON(w, 403, map[string]any{"success": false, "message": "path 不在中继白名单内；鉴权归后端"})
		return
	}

	gw := firstGateway()
	if gw == "" {
		writeJSON(w, 502, map[string]any{
			"success": false,
			"message": "网关未配置 —— $MYNET_GATEWAYS / conf/gateways.json / conf/agent.json.gateway 三处均无；平台面需要 gateway 中继",
		})
		return
	}

	// firstGateway 已归一成 scheme://host；relay 挂在根路径 /v1/relay
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

// firstGateway 返回 gateway **relay 基址**（形如 https://gw.mynet.club，无尾斜杠、无路径）。
//
// 网关来源走 utils.ExplicitGateways（env → conf/gateways.json → agent.json 显式值），
// 只认显式配置：没配就是 502 指因，不吃 LoadAgentCfg 的代码默认。
// ⚠️ 不许在这里各自 os.Getenv —— 曾只读 env+agent.json 漏掉 gateways.json，
// .178 登录中继 502（2026-10-12 实测）。
//
// MYNET_GATEWAYS / agent.json 是 WS 拨号 URL（wss://host/v1/node/connect）。
// relay 是同主机的 HTTPS 面，挂在 **根路径** `/v1/relay`（gateway/server.go），
// 不能把 WS 路径原样拼上去 —— 否则打到
// `https://gw.mynet.club/v1/node/connect/v1/relay`，gateway 返回 Go 默认 404。
// 与 cmd/mynetd/backclient.go 的 newBackClient 同一推导：scheme 映射 + 只取 Host。
func firstGateway() string {
	list, _, ok := utils.ExplicitGateways()
	if !ok || len(list) == 0 {
		return ""
	}
	return wsToRelayBase(list[0])
}

// wsToRelayBase 把 WS/HTTP 网关 URL 归一成 relay 基址 `scheme://host`。
// 非法 URL 返回空串（调用方报 502）。
func wsToRelayBase(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := "http"
	switch u.Scheme {
	case "ws", "http":
		scheme = "http"
	case "wss", "https":
		scheme = "https"
	default:
		return ""
	}
	return scheme + "://" + u.Host
}
