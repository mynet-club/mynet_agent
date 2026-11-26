package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHeartbeatBodiesIncludesProxyIngressOpenAndExcludesFromSign(t *testing.T) {
	metricsJSON := `{
		"hostname": "n180",
		"system": {"cpu_percent": 12.5, "mem_used_mb": 256, "mem_total_mb": 1024, "disk_used_percent": 30.0, "uptime_sec": 3600},
		"vpn": [{"driver": "gnb", "up": true, "interface": "gnb_tun_180", "peers": 3, "peers_up": 3, "addrs": ["10.182.236.180"], "rx_bytes": 100, "tx_bytes": 200}],
		"proxy": {"configured": true, "enabled": true, "role": "server", "ingress_open": true}
	}`

	sendBody, signBody, err := heartbeatBodies("4997342800601732", metricsJSON)
	if err != nil {
		t.Fatalf("heartbeatBodies failed: %v", err)
	}
	if strings.Contains(string(signBody), "node_monitor") || strings.Contains(string(signBody), "proxy_ingress_open") {
		t.Fatalf("signBody must not include node_monitor: %s", signBody)
	}

	var parsed map[string]any
	if err := json.Unmarshal(sendBody, &parsed); err != nil {
		t.Fatalf("unmarshal sendBody: %v", err)
	}
	nm, _ := parsed["node_monitor"].(map[string]any)
	ns, _ := nm["node_self"].(map[string]any)
	if ns["proxy_ingress_open"] != true {
		t.Fatalf("expected proxy_ingress_open=true, got %v", ns["proxy_ingress_open"])
	}
	if ns["proxy_role"] != "server" {
		t.Fatalf("expected proxy_role=server, got %v", ns["proxy_role"])
	}

	// False case: explicitly reports proxy_ingress_open=false (not omitted!)
	metricsOff := strings.Replace(metricsJSON, `"ingress_open": true`, `"ingress_open": false`, 1)
	sendOff, _, err := heartbeatBodies("4997342800601732", metricsOff)
	if err != nil {
		t.Fatalf("heartbeatBodies off failed: %v", err)
	}
	var parsedOff map[string]any
	if err := json.Unmarshal(sendOff, &parsedOff); err != nil {
		t.Fatal(err)
	}
	nsOff := parsedOff["node_monitor"].(map[string]any)["node_self"].(map[string]any)
	v, exists := nsOff["proxy_ingress_open"]
	if !exists || v != false {
		t.Fatalf("expected explicit proxy_ingress_open=false, got exists=%v val=%v", exists, v)
	}
}
