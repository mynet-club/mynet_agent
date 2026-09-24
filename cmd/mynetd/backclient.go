package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// backClient 节点面 HTTP 客户端：**签名请求经 gateway 的 /v1/relay 到 back**。
//
// ── 为什么走 relay 而不是直连 back ────────────────────────────────
// 「back 对节点不可达，gateway 是节点面唯一入口」（mynet-overview.md §15）。
// 而 relay 白名单**已经**包含 `/api/v1/monitor` 与 `/api/v1/heartbeat`
// （gateway/relaypolicy/relaypolicy.go:19-20，注释即「含节点自身通道」），
// 且 ReverseProxy 会逐字转发请求头——所以节点面签名请求可以直接复用这条通路，
// 不需要给 gateway 增加新的面。
//
// ── 签名口径 ────────────────────────────────────────────────────
// back 的 NodeHeartbeatAuthMiddleware::signedPath() 返回 `'/'.$request->path()`，
// 即带前导斜杠的完整路径（如 `/api/v1/monitor/commands`）。
// 消息是 `METHOD\npath\nts\nsha256(body)`（两端算法一致：
// mynet/keys/node_face.go:66 ↔ NodeFaceAuthService.php:62）。
// ⚠️ path 差一个斜杠就全 401，且症状是「鉴权失败」而非「路径写错」，极难定位。
type backClient struct {
	relayBase string // 形如 http://127.0.0.1:8710/v1/relay
	id        *nodeIdentity
	hc        *http.Client
}

// backRequestTimeout 单次节点面请求的超时。
const backRequestTimeout = 20 * time.Second

// newBackClient 从网关的 WS 地址推出 relay 的 HTTP 地址。
//
//	gatewayWS: ws://127.0.0.1:8710/v1/node/connect
//	        →  http://127.0.0.1:8710/v1/relay
func newBackClient(gatewayWS string, id *nodeIdentity) (*backClient, error) {
	u, err := url.Parse(strings.TrimSpace(gatewayWS))
	if err != nil {
		return nil, fmt.Errorf("网关地址非法: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("网关地址缺主机名: %s", gatewayWS)
	}

	scheme := "http"
	switch u.Scheme {
	case "ws":
		scheme = "http"
	case "wss":
		scheme = "https"
	case "http", "https":
		scheme = u.Scheme
	default:
		return nil, fmt.Errorf("网关地址 scheme 无法识别: %s", u.Scheme)
	}

	return &backClient{
		relayBase: scheme + "://" + u.Host + "/v1/relay",
		id:        id,
		hc:        &http.Client{Timeout: backRequestTimeout},
	}, nil
}

// do 发一个签名请求到 back（经 gateway relay）。返回状态码与响应体。
func (c *backClient) do(ctx context.Context, method, backPath string, body []byte) (int, []byte, error) {
	ts, sig := c.id.signer.SignRequest(method, backPath, string(body))

	target := c.relayBase + "?path=" + url.QueryEscape(backPath)
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("构造请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Node-Id", c.id.nodeID)
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Node-Signature", sig)

	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("请求 %s %s: %w", method, backPath, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("读响应: %w", err)
	}
	return resp.StatusCode, raw, nil
}

// pendingCommand 是 back `GET /api/v1/monitor/commands` 返回的单条命令。
//
// 字段对齐 NodeHeartbeatController::selectPendingCommands（:216-222）。
// 注意 back 还会返回一个 `action` 字段（COMMAND_ACTION_MAP 的产物），
// 但那套词表 agent 侧一个都不认识，已裁定删除；这里刻意**不解析**它，
// 只认 `command`。
//
// ID 用 json.Number：back 的 `rt_cmd_id` 是 unsignedBigInteger，JSON 里是数字；
// 但结果回报路径要把它拼进 URL，用 json.Number 可同时容忍数字与字符串两种形态。
type pendingCommand struct {
	ID      json.Number    `json:"id"`
	Command string         `json:"command"`
	Params  map[string]any `json:"params"`
}

type pendingCommandsResponse struct {
	Success  bool             `json:"success"`
	NodeID   json.Number      `json:"node_id"`
	Commands []pendingCommand `json:"commands"`
	Error    string           `json:"error,omitempty"`
}

// pullPendingCommands 拉取待执行命令。
//
// back 每次只返回 1 条并立即标记为「执行中」（selectPendingCommands:209-215），
// 所以调用方应在执行完后再次拉取，直到返回空。
func (c *backClient) pullPendingCommands(ctx context.Context) (*pendingCommandsResponse, error) {
	code, raw, err := c.do(ctx, http.MethodGet, "/api/v1/monitor/commands", nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("拉取待执行命令返回 %d: %s", code, truncateBytes(raw, 200))
	}

	var out pendingCommandsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析待执行命令响应: %w（原文 %s）", err, truncateBytes(raw, 200))
	}
	if !out.Success {
		return nil, fmt.Errorf("back 报告失败: %s", out.Error)
	}
	return &out, nil
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
