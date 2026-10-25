package mynet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCloudDNSBlockPairReplacesBuiltinAndPreservesLocal(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	ads, malware := "# ads\nCloud-Ad.Example\ncloud-ad.example\n", "bad.customer.example\n"
	paths := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/d/plugins/mp/ad-domains.txt":
			w.Write([]byte(ads))
		case "/d/plugins/mp/malware-domains.txt":
			w.Write([]byte(malware))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	writeBlockConfig(t, dnsAdBlockConfig{Block: []string{"local.example"}, Allow: []string{"safe.cloud-ad.example"}})
	refresh := func() {
		t.Helper()
		if e := refreshDNSBlockBaseline(context.Background(), srv.Client(), srv.URL+"/d/plugins/mp/"); e != nil {
			t.Fatal(e)
		}
	}
	refresh()
	b, sig, e := loadDNSAdBlock()
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(paths, []string{"/d/plugins/mp/ad-domains.txt", "/d/plugins/mp/malware-domains.txt"}) {
		t.Fatal(paths)
	}
	if b.source != "ctl" || !b.blocked("img.cloud-ad.example") || !b.blocked("bad.customer.example") || b.blocked("sibling.customer.example") || b.blocked("safe.cloud-ad.example") || !b.blocked("local.example") || b.blocked("doubleclick.net") {
		t.Fatal("cloud replace/overlay semantics")
	}
	baseline, e := loadDNSBlockBaseline()
	if e != nil || len(baseline.Ads) != 1 {
		t.Fatal("persist/reload or dedup failed", e)
	}
	// A valid empty pair withdraws all cloud rules; do not resurrect builtin rules.
	ads, malware = "# empty\n", ""
	refresh()
	b, sig2, e := loadDNSAdBlock()
	if e != nil || sig == sig2 || b.blocked("img.cloud-ad.example") || b.blocked("doubleclick.net") || !b.blocked("local.example") {
		t.Fatal("empty cloud replacement lost authority", e)
	}
}

func TestCloudDNSBlockFailureKeepsWholeLastSnapshot(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	ads, malware := "ad.example\n", "bad.example\n"
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "malware-domains.txt") {
			w.WriteHeader(status)
			w.Write([]byte(malware))
			return
		}
		w.Write([]byte(ads))
	}))
	defer srv.Close()
	refresh := func() error { return refreshDNSBlockBaseline(context.Background(), srv.Client(), srv.URL+"/") }
	if e := refresh(); e != nil {
		t.Fatal(e)
	}
	old, _ := os.ReadFile(dnsBlockBaselinePath())
	ads = "new-ad.example\n"
	for _, tc := range []struct {
		status int
		body   string
	}{{404, "missing"}, {200, "http://malware.example"}, {200, "*.example.com"}, {200, strings.Repeat("a", 1024*1024+1)}} {
		status, malware = tc.status, tc.body
		if e := refresh(); e == nil {
			t.Fatal("bad download accepted")
		}
		raw, _ := os.ReadFile(dnsBlockBaselinePath())
		if string(raw) != string(old) {
			t.Fatal("partial replacement")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := refreshDNSBlockBaseline(ctx, srv.Client(), srv.URL+"/"); e == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestCloudDNSBlockMissingUsesBuiltin(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	b, _, e := loadDNSAdBlock()
	if e != nil || b.source != "builtin" || !b.blocked("doubleclick.net") {
		t.Fatal("no bootstrap fallback")
	}
}
