package mynet

import (
	"encoding/json"
	"testing"

	"github.com/mynet-club/mynet_agent/shared"
)

func TestDeviceInfoFromSnapshot(t *testing.T) {
	raw := []byte(`{
	  "hostname":"MacBook-Pro-3.local","os":"darwin","distribution":"macOS",
	  "kernel":"25.6.0","architecture":"arm64",
	  "interfaces":[{"name":"lo0","mac":"","state":"up"},{"name":"en0","mac":"c2","state":"up"}],
	  "detail":{
	    "system":{"version":"26.6.2"},
	    "firewall":{"type":"pf","enabled":true},
	    "routing":{"interfaces":[{"name":"en0","ips":["10.53.17.231"]},{"name":"lo0","ips":["127.0.0.1"]}]}
	  }
	}`)
	var snap shared.SystemSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	p := deviceInfoFromSnapshot(&snap)
	if p["os_name"] != "macOS" {
		t.Errorf("os_name = %v", p["os_name"])
	}
	if p["os_version"] != "26.6.2" {
		t.Errorf("os_version = %v", p["os_version"])
	}
	if p["kernel_version"] != "25.6.0" {
		t.Errorf("kernel_version = %v", p["kernel_version"])
	}
	if p["system_arch"] != "arm64" || p["cpu_arch"] != "arm64" {
		t.Errorf("arch = %v/%v", p["system_arch"], p["cpu_arch"])
	}
	if p["nic_count"] != 2 {
		t.Errorf("nic_count = %v", p["nic_count"])
	}
	if p["firewall_type"] != "pf" {
		t.Errorf("firewall_type = %v", p["firewall_type"])
	}
	if p["firewall_enabled"] != 1 {
		t.Errorf("firewall_enabled = %v", p["firewall_enabled"])
	}
	ips, _ := p["local_ips"].([]string)
	if len(ips) != 1 || ips[0] != "10.53.17.231" {
		t.Errorf("local_ips = %v（回环不该进）", ips)
	}
}
