package mynet

import (
	"strings"
	"testing"
)

// 铁律：终端只经 gateway（本机 mynetd 中继），不直连云端 api_base。
func TestCloudHTTPBaseIsRelayNotAPiBase(t *testing.T) {
	base := cloudHTTPBase()
	if !strings.HasPrefix(base, "http://127.0.0.1:8096/api/v1/relay?path=") {
		t.Fatalf("CLI 云出口应为中继：%q", base)
	}
	if strings.Contains(base, "api.mynet.club") {
		t.Fatal("CLI 云出口不得直连 api_base")
	}
}

// 建客户端的基址恒为中继出口（2026-10-12 起 newBackClient 连 base 参数都不收，
// 「禁止直连」是编译期形态，不再靠调用方自觉）。
func TestBackClientForIsRelayBase(t *testing.T) {
	c := newBackClientFor(&Credential{Token: "t"})
	if !strings.HasPrefix(c.base, "http://127.0.0.1:8096/api/v1/relay?path=") {
		t.Fatalf("基址应为中继：%q", c.base)
	}
	if !strings.HasPrefix(newBackClient("").base, "http://127.0.0.1:8096/api/v1/relay?path=") {
		t.Fatal("无凭证客户端也必须走中继")
	}
}
