package mynet

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestHookDNSOwnershipChecksActualServicesAndNewInterfaces(t *testing.T) {
	ops, services, writes := fakeSystemDNS(t)
	b := &DNSBackup{Applied: true, Target: "127.0.0.1"}
	for name := range services {
		services[name] = []string{"127.0.0.1"}
	}
	owned, err := hookDNSOwned(b, ops, func() ([]string, error) { return []string{"127.0.0.1"}, nil })
	if err != nil || !owned {
		t.Fatalf("owned=%v err=%v", owned, err)
	}
	services["Wi-Fi"] = nil // 网络重连恢复DHCP，即使备份仍写applied=true也已失去接管。
	owned, err = hookDNSOwned(b, ops, nil)
	if err != nil || owned {
		t.Fatal("changed DNS still reported owned")
	}
	services["Wi-Fi"] = []string{"127.0.0.1"}
	ops.list = func() ([]string, error) { return []string{"Wi-Fi", "new-adapter"}, nil }
	services["new-adapter"] = []string{"192.0.2.53"}
	owned, err = hookDNSOwned(b, ops, nil)
	if err != nil || owned {
		t.Fatal("new network service bypass ignored")
	}
	if *writes != 0 {
		t.Fatal("observation rewrote DNS")
	}
}

func TestHookDNSEffectiveResolverOverrideAndReadFailure(t *testing.T) {
	ops, services, writes := fakeSystemDNS(t)
	for name := range services {
		services[name] = []string{"127.0.0.1"}
	}
	b := &DNSBackup{Applied: true, Target: "127.0.0.1"}
	owned, err := hookDNSOwned(b, ops, func() ([]string, error) { return []string{"203.0.113.53"}, nil })
	if err != nil || owned {
		t.Fatal("VPN effective resolver override ignored")
	}
	_, err = hookDNSOwned(b, ops, func() ([]string, error) { return nil, errors.New("cannot inspect") })
	if err == nil {
		t.Fatal("read failure hidden as healthy")
	}
	if *writes != 0 {
		t.Fatal("read failure rewrote DNS")
	}
}

func TestHookDNSExternalInterruptionStopsAndDoesNotReclaim(t *testing.T) {
	ops, services, writes := fakeSystemDNS(t)
	b := &DNSBackup{Applied: true, Target: "127.0.0.1", Services: map[string][]string{"Wi-Fi": nil, "Ethernet": {"192.0.2.53"}}}
	if err := saveDNSBackup(b); err != nil {
		t.Fatal(err)
	}
	services["Wi-Fi"] = []string{"127.0.0.1"}
	services["Ethernet"] = []string{"203.0.113.53"} // 外部软件的新设置。
	events := make(chan struct{}, 1)
	events <- struct{}{}
	stopped := false
	monitorHookDNS(make(chan struct{}), events, func() (bool, error) { return hookDNSOwned(LoadDNSBackup(), ops, nil) }, recordHookDNSInterruption, func() {
		stopped = true
		if err := restoreSystemDNS(ops); err != nil {
			t.Error(err)
		}
	})
	if !stopped || !hookDNSInterrupted() {
		t.Fatal("external edit did not interrupt session")
	}
	if len(services["Wi-Fi"]) != 0 || !reflect.DeepEqual(services["Ethernet"], []string{"203.0.113.53"}) || *writes != 1 {
		t.Fatalf("external DNS overwritten: %v writes=%d", services, *writes)
	}
	if err := hookDNSResumeAllowed(); err == nil {
		t.Fatal("automatic takeover was not suspended")
	}
	// 包括watcher与service up使用的底层start也不得创建新进程。
	if rc := dnsHookStart(DNSHookListenDefault); rc == 0 {
		t.Fatal("automatic start ignored interruption")
	}
	if err := clearHookDNSInterruption(); err != nil {
		t.Fatal(err)
	}
	if err := hookDNSResumeAllowed(); err != nil {
		t.Fatal("explicit resume could not clear interruption")
	}
}

func TestHookDNSResolverFileOwnershipDetectsReplacement(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	path := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(path, []byte("nameserver 127.0.0.1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	state := resolverFileBackup{Path: path, Applied: []byte("nameserver 127.0.0.1\n")}
	raw, _ := json.Marshal(state)
	b := &DNSBackup{Applied: true, Target: "127.0.0.1", Extra: map[string]string{"resolver_file": string(raw)}}
	owned, err := hookDNSOwned(b, systemDNSOps{}, nil)
	if err != nil || !owned {
		t.Fatal(owned, err)
	}
	if err := os.WriteFile(path, []byte("nameserver 192.0.2.53\n"), 0644); err != nil {
		t.Fatal(err)
	}
	owned, err = hookDNSOwned(b, systemDNSOps{}, nil)
	if err != nil || owned {
		t.Fatal("manager rewrite ignored")
	}
}

func TestHookDNSObservationIsSessionBoundAndExpires(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	if err := os.MkdirAll(filepath.Dir(hookDNSObservationFile()), 0755); err != nil {
		t.Fatal(err)
	}
	saveHookDNSObservation(true, nil)
	if hookDNSObservation(os.Getpid()) != "owned" || hookDNSObservation(os.Getpid()+1) != "" {
		t.Fatal("session observation mismatch")
	}
	saveHookDNSObservation(false, errors.New("read failed"))
	if hookDNSObservation(os.Getpid()) != "unknown" {
		t.Fatal("failed observation displayed healthy")
	}
	raw, _ := json.Marshal(struct {
		PID   int
		State string
		At    time.Time
	}{os.Getpid(), "owned", time.Now().Add(-time.Minute)})
	if err := os.WriteFile(hookDNSObservationFile(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if hookDNSObservation(os.Getpid()) != "unknown" {
		t.Fatal("stale observation displayed healthy")
	}
}

func TestMaintenanceStopPreservesInterruptedPreference(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	t.Setenv("MYNET_CONFIG_HOME", "")
	if err := SetDNSHookWanted(true); err != nil {
		t.Fatal(err)
	}
	if err := recordHookDNSInterruption(); err != nil {
		t.Fatal(err)
	}
	if rc := cmdProxyDNSHook([]string{"stop", "--preserve-intent"}); rc != 0 {
		t.Fatal(rc)
	}
	if !DNSHookWanted() || !hookDNSInterrupted() {
		t.Fatal("maintenance stop discarded preference or interruption")
	}
	if rc := cmdProxyDNSHook([]string{"stop"}); rc != 0 {
		t.Fatal(rc)
	}
	if DNSHookWanted() || hookDNSInterrupted() {
		t.Fatal("explicit disable did not clear preference and interruption")
	}
}
