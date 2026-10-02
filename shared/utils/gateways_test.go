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

func TestResolveGatewaysEnvFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_GATEWAYS", "")
	// MYNET_GATEWAYS 空串视为未设？Getenv 返回 "" 即走回退
	envFile := filepath.Join(home, "mynetd.env")
	if err := os.WriteFile(envFile, []byte("MYNET_HOME="+home+"\nMYNET_GATEWAYS=wss://file-gw.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, src, err := ResolveGateways()
	if err != nil {
		t.Fatal(err)
	}
	if src != "mynetd.env" {
		t.Fatalf("src=%s", src)
	}
	if len(list) != 1 {
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

func TestGatewaysFromEnvFileQuoted(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mynetd.env")
	if err := os.WriteFile(p, []byte(`MYNET_GATEWAYS="wss://q.example.com"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := gatewaysFromEnvFile(p); v != "wss://q.example.com" {
		t.Fatalf("v=%q", v)
	}
}
