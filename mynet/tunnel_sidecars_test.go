package mynet

import (
	"fmt"
	"github.com/mynet-club/mynet_agent/shared/config"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 默认不得劫持：没有意图文件时 DNSHookWanted 必须为 false（D4/§14）。
func TestDNSHookWantedDefaultsFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	if DNSHookWanted() {
		t.Fatal("无意图文件时不得默认开启加速")
	}
}

// 用户开关写入意图；重启后（新进程读文件）仍能读到 —— 隧道启停联动靠它。
func TestDNSHookIntentPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	if err := SetDNSHookWanted(true); err != nil {
		t.Fatal(err)
	}
	if !DNSHookWanted() {
		t.Fatal("SetDNSHookWanted(true) 后应读到 true")
	}
	if err := SetDNSHookWanted(false); err != nil {
		t.Fatal(err)
	}
	if DNSHookWanted() {
		t.Fatal("SetDNSHookWanted(false) 后应读到 false")
	}
}

// 损坏的意图文件不得被当成「已启用」—— fail-closed 到默认不劫持。
func TestDNSHookIntentCorruptIsDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	p := dnsHookIntentFile()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if DNSHookWanted() {
		t.Fatal("损坏意图文件必须按未启用处理")
	}
}

// 未启用时编排必须是 no-op 路径（不 panic、不尝试起进程）。
func TestSyncSidecarsUpSkipsWhenUnconfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MYNET_HOME", home)
	t.Setenv("MYNET_DATA_HOME", "")

	// 不应写任何意图/不应因缺 proxy intent 而 panic。
	SyncSidecarsOnTunnelUp()
	SyncSidecarsOnTunnelDown()

	if DNSHookWanted() {
		t.Fatal("编排不得隐式开启加速意图")
	}
}

func TestAccelerationToggleSharesProxyPolicy(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	for _, on := range []bool{true, false, true} {
		if err := SetDNSHookWanted(on); err != nil {
			t.Fatal(err)
		}
		st, err := LoadProxyState()
		if err != nil || st.Local == nil || st.Local.Policy.Enabled != on {
			t.Fatalf("proxy policy differs from acceleration: %+v %v", st, err)
		}
		if DNSHookWanted() != on {
			t.Fatal("acceleration preference differs")
		}
	}
}

func TestProxyPolicyOverridesStaleHookPreference(t *testing.T) {
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	if err := SetDNSHookWanted(true); err != nil {
		t.Fatal(err)
	}
	// 面板关闭 proxy 后，旧 hook 意图不能在重连时复活加速。
	if err := SetProxyEnabled(false); err != nil {
		t.Fatal(err)
	}
	if DNSHookWanted() {
		t.Fatal("stale hook file revived disabled proxy")
	}
	if err := SetProxyEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !DNSHookWanted() {
		t.Fatal("proxy enable must also request DNS hook")
	}
}

func TestAccelerationStateDistinguishesFailureFromWaiting(t *testing.T) {
	for _, tc := range []struct {
		wanted, alive, tunnel bool
		state                 string
	}{
		{false, false, false, "disabled"}, {true, false, false, "waiting_tunnel"},
		{true, false, true, "failed"}, {true, true, true, "running"},
	} {
		if got := accelerationState(tc.wanted, tc.alive, tc.tunnel); got != tc.state {
			t.Fatalf("%+v: %s", tc, got)
		}
	}
}

func pcAccelerationTestHome(t *testing.T) {
	t.Helper()
	if builtForOpenWrt {
		t.Skip("PC-only lifecycle")
	}
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_CONFIG_HOME", "")
	t.Setenv("MYNET_DATA_HOME", "")
	if err := config.SaveRuntimeMode(runtime.GOOS, false, "pc"); err != nil {
		t.Fatal(err)
	}
}

func TestUserAccelerationEnableWithoutTunnelPreparesPolicy(t *testing.T) {
	pcAccelerationTestHome(t)
	if rc := cmdProxyDNSHook([]string{"start"}); rc != 0 {
		t.Fatalf("start=%d", rc)
	}
	st, err := LoadProxyState()
	if err != nil || st.Local == nil || !st.Local.Policy.Enabled || !DNSHookWanted() {
		t.Fatalf("missing unified preference: %+v %v", st, err)
	}
	if dnsHookRunning() || DNSRestoreNeeded() {
		t.Fatal("waiting must not start worker or own DNS")
	}
	if rc := cmdProxyDNSHook([]string{"stop"}); rc != 0 {
		t.Fatalf("stop=%d", rc)
	}
	if DNSHookWanted() || EffectiveProxyPolicy() != nil {
		t.Fatal("cancel waiting left proxy enabled")
	}
}

func TestLegacyAccelerationChoiceSurvivesMigration(t *testing.T) {
	for _, wanted := range []bool{true, false} {
		t.Run(fmt.Sprint(wanted), func(t *testing.T) {
			pcAccelerationTestHome(t)
			if err := SetProxyEnabled(!wanted); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(dnsHookIntentFile()), 0755); err != nil {
				t.Fatal(err)
			}
			raw := fmt.Sprintf(`{"enabled":%v}`, wanted)
			if err := os.WriteFile(dnsHookIntentFile(), []byte(raw), 0644); err != nil {
				t.Fatal(err)
			}
			if DNSHookWanted() != wanted {
				t.Fatal("legacy last choice lost")
			}
			if err := migratePCAcceleration(); err != nil {
				t.Fatal(err)
			}
			st, err := LoadProxyState()
			if err != nil || st.Local.Policy.Enabled != wanted || DNSHookWanted() != wanted || legacyDNSHookIntent() != nil {
				t.Fatalf("migration failed: %+v %v", st, err)
			}
		})
	}
}
