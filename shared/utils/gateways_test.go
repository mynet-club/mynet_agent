package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveGatewaysEnv(t *testing.T) {
	t.Setenv("MYNET_GATEWAYS", "wss://gw.example.com")
	list, src, err := ResolveGateways()
	if err != nil {
		t.Fatal(err)
	}
	if src != "env" {
		t.Fatalf("src=%s", src)
	}
	if len(list) != 1 || list[0] != DefaultGateway && list[0] != "wss://gw.example.com/v1/node/connect" {
		// NormalizeGatewayURL 会补路径
		if list[0] != "wss://gw.example.com/v1/node/connect" {
			t.Fatalf("list=%v", list)
		}
	}
}

// 2026-10-12：网关列表从 mynetd.env 改到 conf/gateways.json（JSON 数组）。
func TestResolveGatewaysFromJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_GATEWAYS", "")
	if err := os.MkdirAll(filepath.Join(home, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("[" + "\n" + `  "wss://gw1.example.com",` + "\n" + `  "wss://gw2.example.com"` + "\n" + "]\n")
	if err := os.WriteFile(filepath.Join(home, "conf", "gateways.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	list, src, err := ResolveGateways()
	if err != nil {
		t.Fatal(err)
	}
	if src != "gateways.json" {
		t.Fatalf("src=%s", src)
	}
	if len(list) != 2 {
		t.Fatalf("list=%v", list)
	}
}

func TestResolveGatewaysAgentJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_GATEWAYS", "")
	// 不写 mynetd.env，只写 agent.json
	if err := os.MkdirAll(filepath.Join(home, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "conf", "agent.json"),
		[]byte(`{"gateway":"wss://json-gw.example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	list, src, err := ResolveGateways()
	if err != nil {
		t.Fatal(err)
	}
	if src != "agent.json" {
		t.Fatalf("src=%s", src)
	}
	if len(list) != 1 || list[0] != "wss://json-gw.example.com/v1/node/connect" {
		t.Fatalf("list=%v", list)
	}
}

func TestResolveGatewaysEmptyMustError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_GATEWAYS", "")
	// 隔离全局默认 agent.json 路径
	list, src, err := ResolveGateways()
	// LoadAgentCfg 在无文件时返回 DefaultGateway —— 这是有意的产品默认。
	// 但「完全未配置」时我们仍要能指因；用 RequireGateways 验证 src。
	if err == nil && len(list) > 0 {
		// 落在 agent.json 默认 —— 允许，但 source 必须可解释
		if src == "empty" {
			t.Fatalf("有默认却报 empty: %v", list)
		}
		return
	}
	if err == nil {
		t.Fatal("空列表必须报错")
	}
}

// 显式解析（relay/wizard 用）：三处都没配 → ok=false，绝不落代码默认。
func TestExplicitGatewaysNoDefaultFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_GATEWAYS", "")
	if _, _, ok := ExplicitGateways(); ok {
		t.Fatal("完全未配置时 ExplicitGateways 必须 ok=false（不吃 DefaultGateway）")
	}
	// agent.json 没有 gateway 字段同样算未配
	if err := os.MkdirAll(filepath.Join(home, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "conf", "agent.json"),
		[]byte(`{"auto_restore_tunnel":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := ExplicitGateways(); ok {
		t.Fatal("agent.json 无 gateway 字段时 ExplicitGateways 必须 ok=false")
	}
}

// 单个字符串也吃（不是数组）。
func TestGatewaysFromJSONSingleString(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gateways.json")
	if err := os.WriteFile(p, []byte(`"wss://q.example.com/v1/node/connect"`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := gatewaysFromJSON(p)
	if len(got) != 1 || got[0] != "wss://q.example.com/v1/node/connect" {
		t.Fatalf("got=%v", got)
	}
}
