package utils

// 网关解析 —— U3 / core-remediation-plan 主线④，2026-10-12 改配置面。
//
// 问题：心跳/WS/自动拉 bundle **只认 MYNET_GATEWAYS 环境变量**，无回退 →
// systemd/launchd 没写 env 时「静默不启动」（session-pitfalls #16）。
//
// 回退链（spec/paths.md §2.1）：
//   1. $MYNET_GATEWAYS           显式覆盖（逗号分隔多个）
//   2. conf/gateways.json        配置面（install/迁移写入；JSON 字符串数组）
//   3. conf/agent.json.gateway   机器级配置
//   4. 都没有 → 返回空 + 明确错误（调用方必须指因，禁止静默）
//
// ⚠️ `mynetd.env` 已退役（2026-10-12 裁定）：它是"两代同名配置"里的旧一代，
// 不留读取 shim —— 迁移时由 install 把网关写进 conf/gateways.json。

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ExplicitGateways 只认**显式配置**（env → gateways.json → agent.json 显式值），
// 不吃 LoadAgentCfg 的代码默认 —— relay/wizard 这类要「没配就 502 指因」的调用方用它。
// ⚠️ 各调用方必须走本函数或 ResolveGateways，禁止各自 os.Getenv ——
// relay 曾只读 env+agent.json 漏掉 gateways.json，登录中继 502（2026-10-12 .178 实测）。
func ExplicitGateways() (list []string, source string, ok bool) {
	// 1. 环境变量（调试/一次性覆盖）
	if v := os.Getenv("MYNET_GATEWAYS"); v != "" {
		return splitGateways(v), "env", true
	}
	// 2. conf/gateways.json（配置面）
	if g := gatewaysFromJSON(GatewaysFile()); len(g) > 0 {
		return g, "gateways.json", true
	}
	// 3. conf/agent.json 显式 gateway（不是代码默认）
	if g := agentGatewayExplicit(); g != "" {
		return []string{NormalizeGatewayURL(g)}, "agent.json", true
	}
	return nil, "empty", false
}

// ResolveGateways 解析网关列表（已 Normalize）。
// 返回 (list, source, err)。source: env | gateways.json | agent.json | empty。
func ResolveGateways() (list []string, source string, err error) {
	if list, src, ok := ExplicitGateways(); ok {
		return list, src, nil
	}
	// 4. 产品默认（LoadAgentCfg 无文件时补 DefaultGateway）—— 心跳/WS 允许走默认。
	if g := LoadAgentCfg().Gateway; g != "" {
		return []string{NormalizeGatewayURL(g)}, "agent.json", nil
	}
	// 5. 都没有
	return nil, "empty", fmt.Errorf(
		"网关未配置且无回退 —— 心跳/WS/拉 bundle 都不会启动；"+
			"请 export MYNET_GATEWAYS=wss://gw.mynet.club/v1/node/connect，"+
			"或写入 %s（JSON 字符串数组），或配置 conf/agent.json 的 gateway", GatewaysFile())
}

// agentGatewayExplicit 只读 agent.json 文件里写明的 gateway，不读代码默认。
// 用 confDir()（不建目录）：纯解析不该有副作用。
func agentGatewayExplicit() string {
	b, err := os.ReadFile(filepath.Join(confDir(), "agent.json"))
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

// RequireGateways 节点模式必须能解析出网关；否则指因退出。
func RequireGateways() ([]string, string, error) {
	list, src, err := ResolveGateways()
	if err != nil {
		return nil, src, err
	}
	if len(list) == 0 {
		return nil, src, fmt.Errorf("网关列表为空（source=%s）", src)
	}
	return list, src, nil
}

func splitGateways(v string) []string {
	var out []string
	for _, g := range strings.Split(v, ",") {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		out = append(out, NormalizeGatewayURL(g))
	}
	return out
}

// gatewaysFromJSON 读 conf/gateways.json：JSON 字符串数组（也吃单个字符串）。
func gatewaysFromJSON(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []string
	if json.Unmarshal(b, &list) == nil {
		var out []string
		for _, g := range list {
			if g = strings.TrimSpace(g); g != "" {
				out = append(out, NormalizeGatewayURL(g))
			}
		}
		return out
	}
	var one string
	if json.Unmarshal(b, &one) == nil {
		if one = strings.TrimSpace(one); one != "" {
			return []string{NormalizeGatewayURL(one)}
		}
	}
	return nil
}

// SaveGateways 将网关列表原子写入 conf/gateways.json（配置面唯一真相）。
func SaveGateways(list []string) error {
	clean := make([]string, 0, len(list))
	for _, g := range list {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		clean = append(clean, NormalizeGatewayURL(g))
	}
	if len(clean) == 0 {
		return fmt.Errorf("网关列表不能为空")
	}
	target := GatewaysFile()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// ParseGatewayInput 将用户输入的域名或完整地址规整为节点网关地址与主机名。
func ParseGatewayInput(raw string) (string, string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", fmt.Errorf("网关域名不能为空")
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return "", "", fmt.Errorf("网关域名不能包含空白字符")
	}
	if !strings.Contains(s, "://") {
		s = "wss://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", "", fmt.Errorf("网关域名格式无效")
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", "", fmt.Errorf("不支持的协议类型")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1/node/connect"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), u.Host, nil
}

// GatewayConfigSnapshot 返回当前网关配置快照。
func GatewayConfigSnapshot() map[string]any {
	defaultHost := ""
	if u, err := url.Parse(DefaultGateway); err == nil {
		defaultHost = u.Host
	}
	list, src, _ := ExplicitGateways()
	current, host := "", ""
	if len(list) > 0 {
		current = list[0]
		if u, err := url.Parse(current); err == nil {
			host = u.Host
		}
	}
	return map[string]any{
		"gateway":      current,
		"host":         host,
		"gateways":     list,
		"source":       src,
		"env_override": src == "env",
		"default_host": defaultHost,
	}
}

// UpdateGatewayFromInput 校验并落盘单网关配置，返回更新后的快照。
func UpdateGatewayFromInput(raw string) (map[string]any, error) {
	wsURL, _, err := ParseGatewayInput(raw)
	if err != nil {
		return nil, err
	}
	if err := SaveGateways([]string{wsURL}); err != nil {
		return nil, err
	}
	return GatewayConfigSnapshot(), nil
}
