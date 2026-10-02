package localapi

import "testing"

// 2026-09-30 实测：MYNET_GATEWAYS=wss://gw.mynet.club/v1/node/connect 时，
// 旧 firstGateway 只换协议不剥路径，relay 打到
// https://gw.mynet.club/v1/node/connect/v1/relay → gateway Go 默认 404。
// 与 cmd/mynetd/backclient.go 的 newBackClient 同一推导：scheme 映射 + 只取 Host。
func TestWsToRelayBase(t *testing.T) {
	cases := map[string]string{
		"wss://gw.mynet.club/v1/node/connect": "https://gw.mynet.club",
		"wss://gw.mynet.club":                 "https://gw.mynet.club",
		"wss://gw.mynet.club/":                "https://gw.mynet.club",
		"ws://127.0.0.1:8710/v1/node/connect": "http://127.0.0.1:8710",
		"https://gw.mynet.club/v1/node/connect": "https://gw.mynet.club",
		"http://127.0.0.1:8710/v1/relay":      "http://127.0.0.1:8710",
		"":                                    "",
		"not-a-url":                           "",
		"ftp://gw.example.com":                "",
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
