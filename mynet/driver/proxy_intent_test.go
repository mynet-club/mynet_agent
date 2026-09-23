package driver_test

import (
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent-V2/mynet/driver"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

// R5-3：proxy 是业务意图——能力分叉（gnb 有 / wg 无），不在业务对象里写驱动语法
func TestProxyIntentOnlyOnCapableDriver(t *testing.T) {
	gnb := driver.NewGNBDriver(1)
	wg := driver.NewWireGuardDriver("wg0", 1)
	if !gnb.Capabilities().Proxy {
		t.Fatal("gnb must have Proxy")
	}
	if wg.Capabilities().Proxy {
		t.Fatal("wg must not have Proxy")
	}
	// Envelope：gnb 可带 forwarding/proxy；wg intent 无 proxy 字段泄漏到业务
	g := &domain.GnbIntent{
		Identity:   domain.GnbIdentity{NodeID: "1", KeyAlgorithm: "ed25519"},
		Forwarding: &domain.GnbForwarding{},
	}
	text := driver.RenderGNBNodeConf(g)
	if strings.Contains(text, "AllowedIPs") {
		t.Fatal("node.conf must not carry WG terms")
	}
}
