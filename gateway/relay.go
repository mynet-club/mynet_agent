package gateway

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/mynet-club/mynet_agent/gateway/relaypolicy"
)

// relayTimeout 单次中继的超时。用户面是 JSON API 调用，不需要长连接。
const relayTimeout = 30 * time.Second

// handleRelay C-7：用户面 HTTPS 入口。**无身份、不鉴权**——鉴权一律归 back
// （用户面 cookie/会话，节点面 DH+HMAC）。gateway 只做两件事：
//
//  1. 路径白名单（relaypolicy，B3：纯路径，不含任何鉴权语义）
//  2. 内网转发到 back
//
// 契约（spec/control-plane.md:281）：`ANY /v1/relay?path=<back-api-path>`，
// method / body / 头部原样透传，响应原样回写。
//
// ⚠️ 这与 mynetd localapi 侧的 `/api/v1/relay?path=`（GUI → mynetd → gateway）
// 是**两跳不同的东西**：那个是「GUI 信任边界止于本机」的入口，这个是 console
// 与移动端**直连** gateway 的入口（dev-plan.md:199 C-7、:429）。
func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	if s.Upstream == "" {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"success": false,
			"message": "relay 上游未配置（部署配置，不下发）",
		})
		return
	}

	rawPath := r.URL.Query().Get("path")
	if rawPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"message": "缺少 path 参数：/v1/relay?path=<back-api-path>",
		})
		return
	}

	// 白名单默认拒绝。relaypolicy.IsAllowedWith 先查 deny 再查 allow，
	// 且含 % 的路径一律拒（防 %2f 绕过）——不要「优化」掉这两条。
	if !relaypolicy.IsAllowed(rawPath) {
		s.logf("gateway: relay 拒绝（白名单外）path=%s from=%s", rawPath, r.RemoteAddr)
		writeJSON(w, http.StatusForbidden, map[string]any{
			"success": false,
			"message": "path 不在中继白名单内；鉴权归 back",
		})
		return
	}

	target, err := s.resolveUpstream(rawPath)
	if err != nil {
		s.logf("gateway: relay 目标解析失败 path=%s: %v", rawPath, err)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	s.relayProxy(target).ServeHTTP(w, r)
}

// resolveUpstream 把白名单内的 path 拼到 upstream 上，并确认**没有逃逸到别的主机**。
//
// path 是用户可控输入，而 upstream 是内网地址——这是 SSRF 边界。
// 白名单要求前缀是 /api/v1/… 已挡掉大多数花样，但 `//evil.com/api/v1/nodes`
// 这类「协议相对 URL」仍可能改写 host，故解析后再比对一次 scheme+host。
func (s *Server) resolveUpstream(rawPath string) (*url.URL, error) {
	base, err := url.Parse(s.Upstream)
	if err != nil {
		return nil, fmt.Errorf("upstream 地址非法: %w", err)
	}
	if base.Host == "" {
		return nil, fmt.Errorf("upstream 缺少主机名: %s", s.Upstream)
	}

	ref, err := url.Parse(rawPath)
	if err != nil {
		return nil, fmt.Errorf("path 非法: %w", err)
	}

	resolved := base.ResolveReference(ref)
	if resolved.Scheme != base.Scheme || resolved.Host != base.Host {
		return nil, fmt.Errorf("path 试图逃逸到 %s://%s（upstream 是 %s://%s）",
			resolved.Scheme, resolved.Host, base.Scheme, base.Host)
	}
	return resolved, nil
}

// relayProxy 构造一次性反向代理。
//
// 用 httputil.ReverseProxy 而不是手写 http.Client 转发：hop-by-hop 头的剥离、
// chunked、Flush 这些边界它已经处理对了。
func (s *Server) relayProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL = target
			req.Host = target.Host
			// 出站请求由 http.Client 重新计算，留着会报
			// "http: Request.RequestURI can't be set in client requests"。
			req.RequestURI = ""
		},
		Transport: &http.Transport{
			ResponseHeaderTimeout: relayTimeout,
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, e error) {
			s.logf("gateway: relay 上游不可达 path=%s: %v", req.URL.Path, e)
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"success": false,
				"message": "relay 上游不可达；鉴权归 back",
			})
		},
	}
}
