package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mynet-club/mynet_agent/mynetd/commands"
)

func TestEgressIPCacheAndHeartbeatWAN(t *testing.T) {
	c := &egressIPCache{}
	if got := c.Get(); got != "" {
		t.Fatalf("expected empty initial ip, got %q", got)
	}
	if changed, prev := c.Update("203.0.113.10"); !changed || prev != "" {
		t.Fatalf("expected first update changed=true prev='', got %v %q", changed, prev)
	}
	if changed, _ := c.Update("203.0.113.10"); changed {
		t.Fatalf("expected identical ip update changed=false")
	}
	if changed, prev := c.Update("203.0.113.20"); !changed || prev != "203.0.113.10" {
		t.Fatalf("expected ip change 10->20, got %v %q", changed, prev)
	}

	metrics := `{"hostname":"mac","system":{"cpu_percent":12,"mem_used_mb":512,"mem_total_mb":2048,"uptime_sec":100,"disk_used_percent":30},"vpn":[{"driver":"gnb","up":true,"interface":"gnb_tun","peers":1,"peers_up":1,"addrs":["10.1.0.2"]}]}`
	sendBody, signBody, err := heartbeatBodiesWithWAN("123456", metrics, c.Get())
	if err != nil {
		t.Fatalf("heartbeatBodiesWithWAN failed: %v", err)
	}
	if !strings.Contains(string(sendBody), `"wan_ipv4":"203.0.113.20"`) {
		t.Fatalf("sendBody missing wan_ipv4: %s", sendBody)
	}
	if strings.Contains(string(signBody), `"node_monitor"`) {
		t.Fatalf("signBody must exclude node_monitor: %s", signBody)
	}
}

func TestRunBundleLifecycleRespectsWantRunningFalse(t *testing.T) {
	var ran []string
	exec := func(argv []string) (string, string, error) {
		ran = append(ran, strings.Join(argv, " "))
		return "", "", nil
	}
	d := newEventDispatcher("1001", exec, nil, nil)
	d.logf = func(string, ...any) {}
	d.setWantRunning(func() bool { return false })

	d.runBundleLifecycle()

	if len(ran) != 1 || !strings.HasPrefix(ran[0], "refresh-config") {
		t.Fatalf("expected only refresh-config when want_running=false, got %v", ran)
	}
}

func TestExecuteOneUsesConfigExclusiveAndInvalidatesMetrics(t *testing.T) {
	var lockUsed atomic.Bool
	var invalidated atomic.Bool
	var reported commands.Result

	exec := func(argv []string) (string, string, error) {
		return "ok", "", nil
	}
	report := func(_ context.Context, res commands.Result) error {
		reported = res
		return nil
	}
	d := newEventDispatcher("1001", exec, nil, report)
	d.logf = func(string, ...any) {}
	d.setConfigExclusive(func(fn func() error) bool {
		lockUsed.Store(true)
		_ = fn()
		return true
	})
	d.onLifecycleDone = func() {
		invalidated.Store(true)
	}

	d.executeOne(context.Background(), pendingCommand{
		ID:      json.Number("9001"),
		Command: "mynet.restart",
	})

	if !lockUsed.Load() {
		t.Fatalf("expected mynet.restart to acquire config exclusive lock")
	}
	if !invalidated.Load() {
		t.Fatalf("expected mynet.restart to trigger onLifecycleDone")
	}
	if !reported.OK || reported.ID != "9001" {
		t.Fatalf("unexpected reported result: %+v", reported)
	}
}

func TestBundleStoreConcurrentSyncSafe(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	store := newBundleStore()
	valid := []byte(`{"schema_version":2,"generation":"g1","checksum":"abc","generated_at":"2026-11-28T00:00:00Z","node":{"id":"1001"},"driver":"gnb","network":{}}`)
	fetcher := func(_ context.Context, _ string) (int, string, []byte, error) {
		return 200, `"etag-1"`, valid, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Sync(context.Background(), fetcher); err != nil {
				t.Errorf("concurrent Sync error: %v", err)
			}
		}()
	}
	wg.Wait()
}
