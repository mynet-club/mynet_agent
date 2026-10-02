package utils

// 网关解析 —— U3 / core-remediation-plan 主线④。
//
// 问题：心跳/WS/自动拉 bundle **只认 MYNET_GATEWAYS 环境变量**，无回退 →
// systemd/launchd 没写 env 时「静默不启动」（session-pitfalls #16）。
//
// 回退链（spec/paths.md §2）：
//   1. $MYNET_GATEWAYS          显式覆盖（逗号分隔多个）
//   2. $MYNET_HOME/mynetd.env   服务单元加载的 env 文件
//   3. conf/agent.json.gateway  机器级配置
//   4. 都没有 → 返回空 + 明确错误（调用方必须指因，禁止静默）
//
// 对称缺口：proxy 回写已回退读 agent.json，心跳/WS 此前不回退 —— 本函数统一。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GatewaysEnvFile 服务 env 文件路径（install 写入）。
func GatewaysEnvFile() string {
	return filepath.Join(GetMyNetHome(), "mynetd.env")
}

// ResolveGateways 解析网关列表（已 Normalize）。
// 返回 (list, source, err)。source: env | mynetd.env | agent.json | empty。
func ResolveGateways() (list []string, source string, err error) {
	// 1. 环境变量
	if v := os.Getenv("MYNET_GATEWAYS"); v != "" {
		return splitGateways(v), "env", nil
	}
	// 2. mynetd.env（MYNET_HOME 下）
	if v := gatewaysFromEnvFile(GatewaysEnvFile()); v != "" {
		return splitGateways(v), "mynetd.env", nil
	}
	// 2b. 平台服务 env 常见位（未设 MYNET_HOME 时）
	for _, p := range []string{
		"/etc/mynet/mynetd.env",
		"/etc/mynetd.env",
	} {
		if v := gatewaysFromEnvFile(p); v != "" {
			return splitGateways(v), p, nil
		}
	}
	// 3. conf/agent.json
	if g := LoadAgentCfg().Gateway; g != "" {
		return []string{NormalizeGatewayURL(g)}, "agent.json", nil
	}
	// 4. 都没有
	return nil, "empty", fmt.Errorf(
		"MYNET_GATEWAYS 未配置且无回退 —— 心跳/WS/拉 bundle 都不会启动；"+
			"请 export MYNET_GATEWAYS=wss://gw.mynet.club/v1/node/connect，"+
			"或写入 %s，或配置 conf/agent.json 的 gateway", GatewaysEnvFile())
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

// gatewaysFromEnvFile 读 MYNET_GATEWAYS=... 行。
func gatewaysFromEnvFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "MYNET_GATEWAYS=") {
			continue
		}
		v := strings.TrimPrefix(line, "MYNET_GATEWAYS=")
		v = strings.Trim(v, `"'`)
		return v
	}
	return ""
}
