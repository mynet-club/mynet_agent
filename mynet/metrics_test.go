package mynet

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetricsJSONShape(t *testing.T) {
	m := &Metrics{}
	collectSystemMetrics(m)
	m.VPN = append(m.VPN, VPNDriverMetrics{Driver: "gnb", Up: true, Peers: 2})
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{`"driver"`, `"gnb"`, `"system"`} {
		if !strings.Contains(s, key) {
			t.Fatalf("missing %s in %s", key, s)
		}
	}
	// 业务/心跳字段不得含驱动原生术语
	for _, bad := range []string{"AllowedIPs", "node.conf", "wg0", "gnb_tun"} {
		if strings.Contains(s, bad) {
			t.Fatalf("metrics leaked driver term %s: %s", bad, s)
		}
	}
}
