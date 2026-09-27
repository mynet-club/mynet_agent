package utils

// AgentConfig —— 机器级开关与端点（conf/agent.json）。
// 端点**只写配置，不写代码**；代码里的 fallback 也读这份文件。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// AgentCfg conf/agent.json
type AgentCfg struct {
	// APIBase 业务 API（登录/拉节点/拉 bundle）。默认 https://gw.mynet.club
	APIBase string `json:"api_base,omitempty"`
	// Gateway 节点面 WS 网关。默认 wss://gw.mynet.club
	Gateway string `json:"gateway,omitempty"`
	// AutoRestoreTunnel mynetd 启动时按上次状态拉起 gnb|wg
	AutoRestoreTunnel *bool `json:"auto_restore_tunnel,omitempty"`
}

// 默认端点（产品约定 gw.mynet.club）。
// ⚠️ Gateway 是 **WS 拨号 URL**，必须带路径 `/v1/node/connect` ——
// 裸 `wss://gw.mynet.club` 打到 nginx 根会 404（183 实测）。
const (
	DefaultAPIBase = "https://api.mynet.club"
	DefaultGateway = "wss://gw.mynet.club/v1/node/connect"
)

// NormalizeGatewayURL 保证 WS 网关带 `/v1/node/connect`。
//
// 用户/脚本常只写 `wss://gw.mynet.club`（文档与 --gateways 也这么简写），
// 而 gateway 的 WS 面挂在 `/v1/node/connect`（gateway/server.go）。
// 这里在读入口归一，避免「配了网关却 404」的哑坑。
func NormalizeGatewayURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return DefaultGateway
	}
	if !strings.HasPrefix(u, "ws://") && !strings.HasPrefix(u, "wss://") {
		return u // 非 WS 形状原样返回，交给调用方报错
	}
	// 已有路径（不是空或 /）则不动
	rest := strings.TrimPrefix(strings.TrimPrefix(u, "wss://"), "ws://")
	if i := strings.Index(rest, "/"); i >= 0 && i < len(rest)-1 {
		return u
	}
	return strings.TrimRight(u, "/") + "/v1/node/connect"
}

// AgentConfigPath conf/agent.json
func AgentConfigPath() string {
	return filepath.Join(ConfDir(), "agent.json")
}

// LoadAgentCfg 读配置；缺文件/缺字段时补默认。
func LoadAgentCfg() AgentCfg {
	c := AgentCfg{APIBase: DefaultAPIBase, Gateway: DefaultGateway}
	raw, err := os.ReadFile(AgentConfigPath())
	if err != nil {
		return c
	}
	var j AgentCfg
	if err := json.Unmarshal(raw, &j); err != nil {
		return c
	}
	if j.APIBase != "" {
		c.APIBase = j.APIBase
	}
	if j.Gateway != "" {
		c.Gateway = NormalizeGatewayURL(j.Gateway)
	} else {
		c.Gateway = NormalizeGatewayURL(c.Gateway)
	}
	c.AutoRestoreTunnel = j.AutoRestoreTunnel
	return c
}

// SaveAgentCfg 写回（0600）。
func SaveAgentCfg(c AgentCfg) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(AgentConfigPath(), append(b, '\n'), 0o600)
}
