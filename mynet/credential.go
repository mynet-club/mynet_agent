package mynet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Credential 用户凭证 = **会话存储**（账号态，不是节点身份）。
//
// ⚠️ 它从「临时文件」变成会话存储，前提是 back 侧的 refresh 是**真的**。
// 2026-09-25 之前登录响应里有三个装饰品（vendor
// BuildsUserTokenResponse.php:44 / UserAuthController.php:139）：
//
//	refresh_token 是 Str::random(60) 当场编、不入库不校验；
//	auth_token 不传 expiresAt → 永不过期；两个 expires_in 是硬编码元数据。
//
// 那时落盘一个「凭证」是把假的东西存下来，更糟。
// 现在 back 侧已建成真会话（mynet_back f60cf1c：轮转 + 重用检测），
// 本结构才有了对应物：
//
//	auth_token    30 分钟，可续；过期由 doJSON 自动用 refresh_token 换新的
//	refresh_token 7 天，每次续期**轮转**（旧值作废）
//
// 路径与字段与 OpenWrt 侧 LuCI 的 /etc/mynet/conf/credential.json 对齐
// （mynet_openwrt/luasrc/model/mynet/credential.lua 的 M.CRED_FILE），
// 两端读写同一个文件、可以互相接管。LuCI 那边有 `zone_id`（旧 Zone 概念），
// V2 的多租户是 tenant，这里存 tenant_id，不猜 zone。
type Credential struct {
	UserID    string `json:"user_id"`
	UserEmail string `json:"user_email"`
	Token     string `json:"token"`
	// RefreshToken 7 天期，续期时会被**换掉**（轮转），所以每次续期后都要
	// 重新落盘 —— 只存内存里的旧值，下次续期就撞 REFRESH_TOKEN_REUSED，
	// 而那个信号会把整个会话吊销。
	RefreshToken     string `json:"refresh_token"`
	MachineID        string `json:"machine_id,omitempty"`
	TenantID         string `json:"tenant_id,omitempty"`
	APIBaseURL       string `json:"api_base_url,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
	AuthExpiresAt    string `json:"auth_token_expires_at,omitempty"`
	RefreshExpiresAt string `json:"refresh_token_expires_at,omitempty"`
}

func credentialPath() string {
	return filepath.Join(confDir(), "credential.json")
}

// SaveCredential 0600：含**用户 token**，与 mynet.conf 同级敏感。
func SaveCredential(c *Credential) error {
	if err := os.MkdirAll(confDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(credentialPath(), append(b, '\n'), 0o600)
}

// LoadCredential 读不到/解析失败都返回 nil —— 调用方按「未登录」处理并给出提示。
func LoadCredential() *Credential {
	b, err := os.ReadFile(credentialPath())
	if err != nil {
		return nil
	}
	var c Credential
	if err := json.Unmarshal(b, &c); err != nil {
		return nil
	}
	if c.Token == "" {
		return nil
	}

	return &c
}

func RemoveCredential() error {
	err := os.Remove(credentialPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

// ── back HTTP 客户端 ──────────────────────────────────────────
//
// 只做向导/装机需要的少数调用，不做成通用 SDK。
// ⚠️ 时间戳/签名那些是**节点面**的事（cmd/mynetd/nodeface.go），这里走的是
// **用户面**：Bearer token（Sanctum），与浏览器里的 GUI 同一套。

const backHTTPTimeout = 30 * time.Second

type backClient struct {
	base  string
	token string
	hc    *http.Client
	// cred 非 nil 时启用 401 自动续期；续期后会回调 onRefresh 让调用方落盘
	cred      *Credential
	onRefresh func(*Credential) error
}

// newBackClientFor 用已存的凭证建客户端（带自动续期）。
func newBackClientFor(cred *Credential) *backClient {
	c := newBackClient(cred.APIBaseURL, cred.Token)
	c.cred = cred
	c.onRefresh = SaveCredential // 续期后立即落盘新 refresh token

	return c
}

func newBackClient(baseURL, token string) *backClient {
	return &backClient{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		hc:    &http.Client{Timeout: backHTTPTimeout},
	}
}

// refreshSession 换一对新 token 并更新 c.cred。失败返回 error（调用方按未续期处理）。
func (c *backClient) refreshSession() error {
	body, _, err := c.raw("POST", "/api/v1/auth/refresh-token", map[string]string{
		"refresh_token": c.cred.RefreshToken,
	})
	if err != nil {
		return err
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			AuthToken        string `json:"auth_token"`
			RefreshToken     string `json:"refresh_token"`
			AuthExpiresIn    int    `json:"auth_token_expires_in"`
			RefreshExpiresIn int    `json:"refresh_token_expires_in"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil || !env.Success || env.Data.AuthToken == "" {
		return fmt.Errorf("续期响应异常")
	}

	c.cred.Token = env.Data.AuthToken
	c.cred.RefreshToken = env.Data.RefreshToken
	c.token = env.Data.AuthToken
	c.cred.AuthExpiresAt = time.Now().Add(time.Duration(env.Data.AuthExpiresIn) * time.Second).Format(time.RFC3339)
	c.cred.RefreshExpiresAt = time.Now().Add(time.Duration(env.Data.RefreshExpiresIn) * time.Second).Format(time.RFC3339)

	// 轮转后必须落盘，理由见 doJSON 的注释
	if c.onRefresh != nil {
		if err := c.onRefresh(c.cred); err != nil {
			return fmt.Errorf("续期成功但落盘失败（下次会撞重用检测）: %w", err)
		}
	}

	return nil
}

// DoJSON 发请求；401 时用 refresh_token 自动续期一次再重试。
//
// 为什么要在这一层做：access token 只有 30 分钟，而 CLI 的调用是零散的
// （上午 login、下午 nodes）。若把「过期了怎么办」推给每个调用点，
// 迟早有人漏写，症状是「莫名其妙 401」。集中在这里，调用方无感。
//
// ⚠️ 续期会**轮转** refresh token，所以必须立刻落盘新值 —— 只更新内存的话，
// 下次续期拿旧值会撞 REFRESH_TOKEN_REUSED，而那个信号会把整个会话吊销
// （见 SessionRefreshService 的重用检测）。这正是「要轮转就必须有持久化」的原因。
//
// 只重试**一次**：再 401 就是真过期/真吊销，反复续期是拿错误当重试。
func (c *backClient) doJSON(method, path string, body any) ([]byte, int, error) {
	raw, code, err := c.raw(method, path, body)
	if code != 401 || c.cred == nil || c.cred.RefreshToken == "" || path == "/api/v1/auth/refresh-token" {
		return raw, code, err
	}
	if rerr := c.refreshSession(); rerr != nil {
		return raw, code, err // 原错误更有诊断价值
	}

	return c.raw(method, path, body)
}

func (c *backClient) raw(method, path string, body any) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return raw, resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	return raw, resp.StatusCode, nil
}

// envelope back 统一响应信封（BaseApiController::successResponse）。
type envelope struct {
	Success bool            `json:"success"`
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *backClient) getEnveloped(path string, out any) error {
	raw, code, err := c.doJSON("GET", path, nil)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("解析响应失败（HTTP %d）: %w", code, err)
	}
	if !env.Success {
		return fmt.Errorf("back 返回失败（HTTP %d）: %s", code, env.Message)
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("解析 data 失败: %w", err)
		}
	}

	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "…"
}

// anyToStr 把 JSON 解出来的值转成字符串。
// back 的 user_id / tenant_id 是 16 位雪花 ID，Go 的 encoding/json 会把它
// 解成 float64（超过 2^53 就丢精度），所以凡是数字一律走 FormatInt。
func anyToStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		// json 数字默认落到这里
		return strconv.FormatInt(int64(t), 10)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

// machineID 机器标识。用 hostname —— 与 LuCI 侧 util.get_machine_id() 的
// 同类用途（记录「这份凭证属于哪台机器」），不是密码学身份。
// 真正的节点身份在 conf/mynet.conf 的 NODE_ID + 密钥里。
func machineID() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}

	return h
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}
