package mynet

import (
	"strings"
	"testing"
)

// include 内容契约：持久化语义（fw4 reload 重放 NAT）+ 幂等（先查后加）+
// dev 注入安全（非法字符拒）。
func TestFirewallIncludeScript(t *testing.T) {
	body, err := FirewallIncludeScript("gnb_tun")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`DEV="gnb_tun"`,
		"masquerade",
		"grep -q", // 幂等判据
		"exit 0",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("include 缺 %q:\n%s", want, body)
		}
	}

	if _, err := FirewallIncludeScript("bad;rm -rf /"); err == nil {
		t.Fatal("非法接口名必须拒")
	}
	if _, err := FirewallIncludeScript("waytoolongdevicename"); err == nil {
		t.Fatal("超长接口名必须拒")
	}
	// 空 dev = 骨架（bundle 未就绪），不得报错
	if body, err := FirewallIncludeScript(""); err != nil || !strings.Contains(body, `DEV=""`) {
		t.Fatalf("空 dev 应出骨架: %v\n%s", err, body)
	}
}
