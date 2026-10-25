package mynet

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

type sessionHarness struct {
	s    *pcProxySession
	fib  pcRouteView
	ops  []string
	fail bool
}

func newSessionHarness(t *testing.T) *sessionHarness {
	t.Helper()
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	h := &sessionHarness{s: newPCProxySession(), fib: pcRouteView{"0.0.0.0/0": "en0"}}
	h.s.inspect = func() (pcRouteView, error) {
		out := pcRouteView{}
		for p, d := range h.fib {
			out[p] = d
		}
		return out, nil
	}
	h.s.route = func(action, prefix, tun string) error {
		h.ops = append(h.ops, action+" "+prefix+" "+tun)
		if h.fail {
			return errors.New("route failed")
		}
		if action == "del" {
			delete(h.fib, prefix)
		} else {
			h.fib[prefix] = tun
		}
		return nil
	}
	return h
}
func sessionRules(names ...string) *dnsHookRules {
	r := &dnsHookRules{proxied: map[string]bool{}, foreign: []string{"10.0.0.1"}, domestic: []string{"223.5.5.5"}}
	for _, name := range names {
		r.proxied[name] = true
	}
	return r
}
func mustObserve(t *testing.T, h *sessionHarness, r *dnsHookRules, name string, now time.Time, ips ...string) {
	t.Helper()
	if err := h.s.observe(name, ips, now.Add(time.Minute), now, r, "utun4"); err != nil {
		t.Fatal(err)
	}
}

func TestPCSessionOnDemandSharedIPAndRemoval(t *testing.T) {
	h := newSessionHarness(t)
	now := time.Now()
	r := sessionRules("a.test", "b.test")
	if err := h.s.sync(r, "utun4", now); err != nil {
		t.Fatal(err)
	}
	if len(h.ops) != 0 {
		t.Fatal("white list alone must not load routes")
	}
	mustObserve(t, h, r, "a.test", now, "192.0.2.4")
	mustObserve(t, h, r, "b.test", now, "192.0.2.4")
	if len(h.ops) != 1 {
		t.Fatalf("shared IP installed twice: %v", h.ops)
	}
	if err := h.s.sync(sessionRules("b.test"), "utun4", now); err != nil {
		t.Fatal(err)
	}
	if h.fib["192.0.2.4/32"] != "utun4" {
		t.Fatal("another domain still owns IP")
	}
	if err := h.s.sync(sessionRules(), "utun4", now); err != nil {
		t.Fatal(err)
	}
	if len(h.s.Routes) != 0 || len(h.s.Domains) != 0 || len(h.fib) != 1 {
		t.Fatalf("not released: %+v", h)
	}
}

func TestPCSessionExpiresAndReconcilesMergedPrefixes(t *testing.T) {
	h := newSessionHarness(t)
	now := time.Now()
	r := sessionRules("a.test", "b.test")
	mustObserve(t, h, r, "a.test", now, "192.0.2.4")
	mustObserve(t, h, r, "b.test", now.Add(30*time.Second), "192.0.2.5")
	if h.fib["192.0.2.4/31"] != "utun4" {
		t.Fatalf("not merged: %v", h.fib)
	}
	if _, ok := h.fib["192.0.2.4/32"]; ok {
		t.Fatal("obsolete host route left behind")
	}
	if err := h.s.sync(r, "utun4", now.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	if h.fib["192.0.2.5/32"] != "utun4" || len(h.fib) != 2 {
		t.Fatalf("expiry did not shrink: %v", h.fib)
	}
	if err := h.s.sync(r, "utun4", now.Add(91*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(h.fib) != 1 {
		t.Fatalf("expired route remains: %v", h.fib)
	}
}

func TestPCSessionRepairReconnectAndRelease(t *testing.T) {
	h := newSessionHarness(t)
	now := time.Now()
	r := sessionRules("a.test")
	mustObserve(t, h, r, "a.test", now, "192.0.2.4")
	delete(h.fib, "192.0.2.4/32")
	mustObserve(t, h, r, "a.test", now, "192.0.2.4")
	if h.fib["192.0.2.4/32"] != "utun4" {
		t.Fatal("lost route not repaired")
	}
	if err := h.s.sync(r, "utun9", now); err != nil {
		t.Fatal(err)
	}
	if h.fib["192.0.2.4/32"] != "utun9" {
		t.Fatal("new interface not used")
	}
	if err := h.s.sync(r, "", now); err != nil {
		t.Fatal(err)
	}
	if len(h.s.Routes) != 0 || len(h.s.Domains) == 0 {
		t.Fatal("disconnect must release routes and retain valid leases")
	}
	if err := h.s.sync(r, "utun9", now); err != nil {
		t.Fatal(err)
	}
	if err := h.s.release(now); err != nil {
		t.Fatal(err)
	}
	if len(h.fib) != 1 || len(h.s.Routes) != 0 {
		t.Fatal("close left routes")
	}
	fresh := newPCProxySession()
	if err := fresh.load("utun9"); err != nil {
		t.Fatal(err)
	}
	if len(fresh.Domains) != 1 || len(fresh.Routes) != 0 {
		t.Fatalf("restart state: %+v", fresh)
	}
	fresh.route = h.s.route
	fresh.inspect = h.s.inspect
	if err := fresh.sync(r, "utun10", now); err != nil {
		t.Fatal(err)
	}
	if h.fib["192.0.2.4/32"] != "utun10" {
		t.Fatal("restart failed to reload valid lease")
	}
}

func TestPCSessionPreservesUnownedRoutes(t *testing.T) {
	h := newSessionHarness(t)
	now := time.Now()
	r := sessionRules("a.test")
	h.fib["192.0.2.0/24"] = "utun4"
	mustObserve(t, h, r, "a.test", now, "192.0.2.4")
	if len(h.ops) != 0 || len(h.s.Routes) != 0 {
		t.Fatal("must not take ownership of externally routed IP")
	}
	if err := h.s.release(now); err != nil {
		t.Fatal(err)
	}
	if h.fib["192.0.2.0/24"] != "utun4" {
		t.Fatal("deleted external route")
	}
}

func TestPCSessionDoesNotDeleteRouteReassignedByUser(t *testing.T) {
	h := newSessionHarness(t)
	now := time.Now()
	r := sessionRules("a.test")
	mustObserve(t, h, r, "a.test", now, "192.0.2.4")
	h.fib["192.0.2.4/32"] = "other-vpn"
	if err := h.s.release(now); err != nil {
		t.Fatal(err)
	}
	if h.fib["192.0.2.4/32"] != "other-vpn" {
		t.Fatal("deleted user route")
	}
}

func TestPCSessionInstallFailureRetriesAndRemainsOwned(t *testing.T) {
	h := newSessionHarness(t)
	now := time.Now()
	r := sessionRules("a.test")
	h.fail = true
	if err := h.s.observe("a.test", []string{"192.0.2.4"}, now.Add(time.Minute), now, r, "utun4"); err == nil {
		t.Fatal("failure hidden")
	}
	fresh := newPCProxySession()
	if err := fresh.load("utun4"); err != nil {
		t.Fatal(err)
	}
	if len(fresh.Domains) != 1 || len(fresh.Routes) != 1 {
		t.Fatal("failed install lost recovery journal")
	}
	h.fail = false
	if err := h.s.sync(r, "utun4", now); err != nil {
		t.Fatal(err)
	}
	if h.fib["192.0.2.4/32"] != "utun4" {
		t.Fatal("retry failed")
	}
}

func TestPCSessionBoundsActiveSetWithoutEvictingLiveIPs(t *testing.T) {
	h := newSessionHarness(t)
	now := time.Now()
	r := sessionRules("a.test", "b.test")
	var ips []string
	for i := 0; i < pcPinMaxIPs; i++ {
		ips = append(ips, fmt.Sprintf("198.18.%d.%d", i/256, i%256))
	}
	mustObserve(t, h, r, "a.test", now, ips...)
	before := len(h.ops)
	if err := h.s.observe("b.test", []string{"192.0.2.9"}, now.Add(time.Minute), now, r, "utun4"); err == nil {
		t.Fatal("over-capacity accepted")
	}
	if len(h.s.Domains) != 1 || len(h.ops) != before {
		t.Fatal("capacity failure evicted active routes")
	}
	if err := h.s.sync(r, "utun4", now.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	mustObserve(t, h, r, "b.test", now.Add(62*time.Second), "192.0.2.9")
}

func TestPCSessionMigratesLegacyPinsWithoutPermanentLeases(t *testing.T) {
	h := newSessionHarness(t)
	legacy := map[string]any{"ips": map[string]time.Time{"192.0.2.4": time.Now(), "192.0.2.5": time.Now()}}
	raw, _ := json.Marshal(legacy)
	if err := os.MkdirAll(filepath.Dir(dnsHookPinFile()), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dnsHookPinFile(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := SavePCPinIPs([]string{"192.0.2.4/31"}); err != nil {
		t.Fatal(err)
	}
	h.fib["192.0.2.4/32"] = "utun4"
	h.fib["192.0.2.4/31"] = "utun4"
	if err := h.s.load("utun4"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.sync(sessionRules("a.test"), "utun4", time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(h.fib) != 1 || len(h.s.Domains) != 0 || len(LoadPCPinIPs()) != 0 {
		t.Fatal("legacy state leaked into active set")
	}
}

func TestPCRouteViewChecksEntirePrefix(t *testing.T) {
	v := pcRouteView{"0.0.0.0/0": "en0", "192.0.2.4/32": "utun4"}
	if v.covers("192.0.2.4/31", "utun4") {
		t.Fatal("first IP does not prove whole prefix")
	}
	v["192.0.2.5/32"] = "utun4"
	if !v.covers("192.0.2.4/31", "utun4") {
		t.Fatal("both IPs covered")
	}
}
func TestPCNetstatParsesDarwinAbbreviations(t *testing.T) {
	v, err := parsePCNetstat("Routing tables\nInternet:\nDestination Gateway Flags Netif Expire\ndefault 192.168.1.1 UGScg en0\n192.0.2.4  link#20 UH utun4\n198.18/15 link#20 U utun4\n")
	if err != nil {
		t.Fatal(err)
	}
	want := pcRouteView{"0.0.0.0/0": "en0", "192.0.2.4/32": "utun4", "198.18.0.0/15": "utun4"}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("%v", v)
	}
}

func TestHookInterfaceAcceptsWindowsIndexToken(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil || len(ifaces) == 0 {
		t.Fatalf("interfaces: %v", err)
	}
	original := ifaces[0]
	byIndex, err := hookInterface(strconv.Itoa(original.Index))
	if err != nil || byIndex.Name != original.Name {
		t.Fatalf("numeric interface token not resolved: %v %v", byIndex, err)
	}
	byName, err := hookInterface(original.Name)
	if err != nil || byName.Index != original.Index {
		t.Fatalf("named interface token not resolved: %v %v", byName, err)
	}
}

func TestPCSessionCustomIPsFollowIntentWithoutDNS(t *testing.T) {
	h := newSessionHarness(t)
	r := sessionRules()
	r.customIPs = []string{"192.0.2.4", "192.0.2.5"}
	now := time.Now()
	if err := h.s.sync(r, "utun4", now); err != nil {
		t.Fatal(err)
	}
	if h.fib["192.0.2.4/31"] != "utun4" || len(h.s.Domains) != 0 {
		t.Fatal("explicit IPs should load without DNS queries")
	}
	if err := h.s.sync(r, "utun4", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(h.ops) != 1 {
		t.Fatal("explicit IP intent should remain stable")
	}
	r.customIPs = nil
	if err := h.s.sync(r, "utun4", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(h.fib) != 1 || len(h.s.Routes) != 0 {
		t.Fatal("removed custom IP routes remain")
	}
}
