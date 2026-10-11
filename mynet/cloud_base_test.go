package mynet

import (
	"strings"
	"testing"
)

// 铁律：终端只经 gateway（本机 mynetd 中继），不直连 api_base。
func TestCloudHTTPBaseIsRelayNotAPiBase(t *testing.T) {
	base := cloudHTTPBase()
	if !strings.HasPrefix(base, "http://127.0.0.1:8096/api/v1/relay?path=") {
		t.Fatalf("CLI 云出口应为中继：%q", base)
	}
	if strings.Contains(base, "api.mynet.club") {
		t.Fatal("CLI 云出口不得直连 api_base")
	}
}

// 即便凭证里记着 api_base，建客户端也必须用中继出口（防回归）。
func TestBackClientForIgnoresAPIBaseURL(t *testing.T) {
	cred := &Credential{APIBaseURL: "https://api.mynet.club", Token: "t"}
	c := newBackClientFor(cred)
	if strings.Contains(c.base, "api.mynet.club") {
		t.Fatalf("不得用凭证里的 api_base 作 HTTP 基址：%q", c.base)
	}
	if !strings.HasPrefix(c.base, "http://127.0.0.1:8096/api/v1/relay?path=") {
		t.Fatalf("基址应为中继：%q", c.base)
	}
}
