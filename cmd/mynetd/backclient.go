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
	code, _, raw, err := c.doSignedFull(ctx, method, backPath, sendBody, signBody, nil)
	return code, raw, err
}

// doSignedFull 与 doSigned 相同，但可带额外请求头、并把响应头一并返回。
//
// 只有 bundle 拉取需要这两样：请求要带 `If-None-Match`（省掉重复下发），
// 响应要读 `ETag`（存下来供下次条件请求）。其余端点走 doSigned 即可。
func (c *backClient) doSignedFull(
	ctx context.Context,
	method, backPath string,
	sendBody, signBody []byte,
	extraHeaders map[string]string,
) (int, http.Header, []byte, error) {
	ts, sig := c.id.signer.SignRequest(method, backPath, string(signBody))

	target := c.relayBase + "?path=" + url.QueryEscape(backPath)
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(sendBody))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("构造请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Node-Id", c.id.nodeID)
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Node-Signature", sig)
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("请求 %s %s: %w", method, backPath, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("读响应: %w", err)
	}
	return resp.StatusCode, resp.Header, raw, nil
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

// bundlePath 是 back 下发 BundleEnvelope 的端点（R8 线格式，schema v2）。
//
// ⚠️ 路径必须带前导斜杠，且要与 back 的 signedPath() 完全一致 ——
// 差一个斜杠就全 401，而症状是「鉴权失败」不是「路径写错」（见本文件顶部注释）。
const bundlePath = "/api/v1/monitor/node/bundle"

// fetchBundle 拉取本节点的配置包。
//
// 契约对齐 mynet_back 的 NodeMonitorController::getNodeBundle：
//   - 头 X-Node-Id / X-Node-Signature，与其它节点面端点同一套签名
//   - 可选 If-None-Match: "<etag>"，命中则 **304 且无响应体**
//   - 200 时响应头带 ETag，体是 Envelope JSON
//   - back 在无已发布修订时会自动发布一版，所以首拉不会落空
//
// 返回的 code 交给调用方分支（200 / 304 / 其它），这里不擅自把 304 当错误 ——
// 「没变化」是正常结果，不是失败。
func (c *backClient) fetchBundle(ctx context.Context, ifNoneMatch string) (code int, etag string, body []byte, err error) {
	var extra map[string]string
	if ifNoneMatch != "" {
		extra = map[string]string{"If-None-Match": ifNoneMatch}
	}
	code, hdr, raw, err := c.doSignedFull(ctx, http.MethodGet, bundlePath, nil, nil, extra)
	if err != nil {
		return 0, "", nil, err
	}
	return code, hdr.Get("ETag"), raw, nil
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
