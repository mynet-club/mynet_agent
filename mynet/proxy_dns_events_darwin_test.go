package mynet

import "testing"

func TestParseMacPrimaryDNSDoesNotTrustSavedServiceSettings(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"DNS configuration\nresolver #1\n nameserver[0] : 127.0.0.1\nresolver #2\n nameserver[0] : 192.0.2.53\n", "127.0.0.1"},
		{"resolver #1\n domain : private.test\n nameserver[0] : 192.0.2.53\n flags : Supplemental\nresolver #2\n nameserver[0] : 127.0.0.1\n", "127.0.0.1"},
		{"resolver #1\n nameserver[0] : 203.0.113.53\nresolver #2\n nameserver[0] : 127.0.0.1\n", "203.0.113.53"},
	} {
		got, err := parseMacPrimaryDNS(tc.text)
		if err != nil || len(got) != 1 || got[0] != tc.want {
			t.Fatalf("%v %v", got, err)
		}
	}
	if _, err := parseMacPrimaryDNS("no DNS available"); err == nil {
		t.Fatal("missing resolver reported healthy")
	}
}

func TestMacOwnershipMonitorOnlyChecksConnectedServices(t *testing.T) {
	text := "(1) Ethernet\n(Hardware Port: USB LAN, Device: en0)\n(2) Wi-Fi\n(Hardware Port: Wi-Fi, Device: en1)\n(*) Disabled\n(Hardware Port: Other, Device: en2)\n"
	got := parseMacActiveDNSServices(text, map[string]bool{"en0": true, "en2": true})
	if len(got) != 1 || got[0] != "Ethernet" {
		t.Fatalf("inactive/disabled service checked: %v", got)
	}
	got = parseMacActiveDNSServices(text, map[string]bool{"en1": true})
	if len(got) != 1 || got[0] != "Wi-Fi" {
		t.Fatalf("network switch not detected: %v", got)
	}
}
