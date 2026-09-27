package utils

import "testing"

func TestNormalizeGatewayURL(t *testing.T) {
	cases := map[string]string{
		"wss://gw.mynet.club":                 "wss://gw.mynet.club/v1/node/connect",
		"wss://gw.mynet.club/":                "wss://gw.mynet.club/v1/node/connect",
		"wss://gw.mynet.club/v1/node/connect": "wss://gw.mynet.club/v1/node/connect",
		"ws://127.0.0.1:8710/v1/node/connect": "ws://127.0.0.1:8710/v1/node/connect",
		"":                                    DefaultGateway,
	}
	for in, want := range cases {
		if got := NormalizeGatewayURL(in); got != want {
			t.Fatalf("NormalizeGatewayURL(%q)=%q want %q", in, got, want)
		}
	}
}
