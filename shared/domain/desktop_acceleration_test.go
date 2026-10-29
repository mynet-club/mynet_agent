package domain

import "testing"

func TestNormalizeDesktopDomains(t *testing.T) {
	got, err := NormalizeDesktopDomains([]string{" Example.COM. ", "*.Internal.Example", "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"*.internal.example", "example.com"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNormalizeDesktopDomainsRejectsInvalid(t *testing.T) {
	if _, err := NormalizeDesktopDomains([]string{"https://example.com"}); err == nil {
		t.Fatal("URL must not be accepted as a domain")
	}
}

func TestChooseDesktopDNSSourceUsesPriorityAndHealth(t *testing.T) {
	candidates := map[string]DesktopDNSSource{
		DesktopDNSTenant:      {Name: DesktopDNSTenant, Address: "tenant", Healthy: false},
		DesktopDNSPeer:        {Name: DesktopDNSPeer, Address: "peer", Healthy: true},
		DesktopDNSCleanPublic: {Name: DesktopDNSCleanPublic, Address: "public", Healthy: true},
	}
	got, ok := ChooseDesktopDNSSource([]string{DesktopDNSTenant, DesktopDNSPeer, DesktopDNSCleanPublic}, candidates)
	if !ok || got.Name != DesktopDNSPeer {
		t.Fatalf("got %+v, ok=%v", got, ok)
	}
}

func TestChooseDesktopDNSSourceFailsClosed(t *testing.T) {
	if _, ok := ChooseDesktopDNSSource([]string{DesktopDNSCleanPublic}, map[string]DesktopDNSSource{}); ok {
		t.Fatal("must not select an unavailable DNS source")
	}
}
