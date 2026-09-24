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
	return c.doSigned(ctx, method, backPath, body, body)
}

// doSigned 与 do 相同，但**签名体与发送体可以不同**。
//
// 只有心跳需要这个：back 的 `NodeHeartbeatAuthMiddleware` 在验签前会把
// `node_monitor` 置 nil 再用 PHP 重编码（`spec/node-keying.md` §4.3 —— 它体量大
// 且不参与签名）。所以签的是不含它的那份、发的是完整的那份。
// 拿完整体去签，两端 sha256(body) 永远不等，症状是清一色 401。
//
// 其余端点两者相同，走 do 即可。
func (c *backClient) doSigned(ctx context.Context, method, backPath string, sendBody, signBody []byte) (int, []byte, error) {
	ts, sig := c.id.signer.SignRequest(method, backPath, string(signBody))

	target := c.relayBase + "?path=" + url.QueryEscape(backPath)
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(sendBody))
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

// reportHeartbeat 上报一次心跳（V4-2）。
//
// sendBody 是完整请求体，signBody 是去掉 node_monitor 的那份 —— 两者的区别
// 见 doSigned 的注释。这里不自己拼 JSON，组装在 heartbeat.go。
func (c *backClient) reportHeartbeat(ctx context.Context, sendBody, signBody []byte) error {
	code, raw, err := c.doSigned(ctx, http.MethodPost, "/api/v1/monitor/heartbeat", sendBody, signBody)
	if err != nil {
		return err
	}
	// 201 也接受：back 现在返 200，但「创建一条心跳记录」语义上是 201，
	// 哪天改了不该让节点侧集体报错。
	if code != http.StatusOK && code != http.StatusCreated {
		return fmt.Errorf("心跳上报返回 %d: %s", code, truncateBytes(raw, 300))
	}
	return nil
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
