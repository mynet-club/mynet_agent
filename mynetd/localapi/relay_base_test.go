package localapi

import (
	"os"
	"path/filepath"
	"testing"
)

// 2026-09-30 实测：MYNET_GATEWAYS=wss://gw.mynet.club/v1/node/connect 时，
// 旧 firstGateway 只换协议不剥路径，relay 打到
// https://gw.mynet.club/v1/node/connect/v1/relay → gateway Go 默认 404。
// 与 cmd/mynetd/backclient.go 的 newBackClient 同一推导：scheme 映射 + 只取 Host。
func TestWsToRelayBase(t *testing.T) {
	cases := map[string]string{
		"wss://gw.mynet.club/v1/node/connect":   "https://gw.mynet.club",
		"wss://gw.mynet.club":                   "https://gw.mynet.club",
		"wss://gw.mynet.club/":                  "https://gw.mynet.club",
		"ws://127.0.0.1:8710/v1/node/connect":   "http://127.0.0.1:8710",
		"https://gw.mynet.club/v1/node/connect": "https://gw.mynet.club",
		"http://127.0.0.1:8710/v1/relay":        "http://127.0.0.1:8710",
		"":                                      "",
		"not-a-url":                             "",
		"ftp://gw.example.com":                  "",
	}
	for in, want := range cases {
		if got := wsToRelayBase(in); got != want {
			t.Errorf("wsToRelayBase(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFirstGatewayStripsWSPath(t *testing.T) {
	t.Setenv("MYNET_GATEWAYS", "wss://gw.mynet.club/v1/node/connect")
	if got := firstGateway(); got != "https://gw.mynet.club" {
		t.Fatalf("firstGateway()=%q want https://gw.mynet.club", got)
	}

	t.Setenv("MYNET_GATEWAYS", "ws://127.0.0.1:8710/v1/node/connect,wss://other/v1/node/connect")
	if got := firstGateway(); got != "http://127.0.0.1:8710" {
		t.Fatalf("firstGateway() first-of-list=%q want http://127.0.0.1:8710", got)
	}
}

// 回归 2026-10-12 .178：conf/gateways.json 是网关来源之一，
// 旧 firstGateway 只读 env+agent.json → 登录中继 502「未配置 MYNET_GATEWAYS」。
func TestFirstGatewayFromGatewaysJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_GATEWAYS", "")
	if err := os.MkdirAll(filepath.Join(home, "conf"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "conf", "gateways.json"),
		[]byte(`["wss://gw.mynet.club/v1/node/connect"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := firstGateway(); got != "https://gw.mynet.club" {
		t.Fatalf("firstGateway()=%q want https://gw.mynet.club（gateways.json 回退）", got)
	}
}

// 显式解析不吃代码默认：三处都没配就是没配（502 指因）。
func TestFirstGatewayUnconfiguredIsEmpty(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_GATEWAYS", "")
	if got := firstGateway(); got != "" {
		t.Fatalf("firstGateway()=%q want \"\"（未显式配置不得落代码默认）", got)
	}
}
