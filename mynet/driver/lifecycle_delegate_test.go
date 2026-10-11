package driver

import (
	"errors"
	"strings"
	"testing"
)

// 平台服务管理器在（OpenWrt/procd）时，两个驱动都必须**交给它**，不得自建
// （gnb 自建 spawn 绕过 procd → 双 gnb 温床；wg exec wg-quick → OpenWrt 上根本没这个命令）。
func TestLifecycleDelegatesToPlatformOwner(t *testing.T) {
	old := platformServiceOwnerFn
	defer func() { platformServiceOwnerFn = old }()
	platformServiceOwnerFn = func() string { return "/etc/init.d/mynet" }

	t.Run("gnb", func(t *testing.T) {
		var calls []string
		d := NewGNBDriver(1)
		d.ConfDir = t.TempDir()
		d.Exec = func(n string, a ...string) ([]byte, error) {
			calls = append(calls, n+" "+strings.Join(a, " "))
			return nil, nil
		}
		d.Spawn = func(string, ...string) error { t.Fatal("委托时不该自建 spawn gnb"); return nil }

		if err := d.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := d.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if err := d.Reload(); err != nil {
			t.Fatalf("Reload: %v", err)
		}
		got := strings.Join(calls, "\n")
		for _, want := range []string{
			"/etc/init.d/mynet start", "/etc/init.d/mynet stop", "/etc/init.d/mynet restart",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("缺 %q：\n%s", want, got)
			}
		}
	})

	t.Run("wireguard", func(t *testing.T) {
		var calls []string
		d := NewWireGuardDriver("wg0", 0)
		d.Exec = func(n string, a ...string) ([]byte, error) {
			calls = append(calls, n+" "+strings.Join(a, " "))
			// 让热同步先失败（接口"不在"）→ 才会走到委托
			if n == "wg" {
				return nil, errors.New("no such device")
			}
			return nil, nil
		}
		if err := d.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := d.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if err := d.Reload(); err != nil {
			t.Fatalf("Reload: %v", err)
		}
		got := strings.Join(calls, "\n")
		for _, want := range []string{
			"/etc/init.d/mynet start", "/etc/init.d/mynet stop", "/etc/init.d/mynet restart",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("缺 %q：\n%s", want, got)
			}
		}
		if strings.Contains(got, "wg-quick") {
			t.Fatalf("委托时不该 exec wg-quick（OpenWrt 没这个命令）：\n%s", got)
		}
	})
}

// 没有平台 owner（systemd/launchd/Windows/容器）→ 行为不变：gnb 走自建、wg 走 wg-quick。
func TestLifecycleFallsThroughWithoutPlatformOwner(t *testing.T) {
	old := platformServiceOwnerFn
	defer func() { platformServiceOwnerFn = old }()
	platformServiceOwnerFn = func() string { return "" }

	var calls []string
	d := NewWireGuardDriver("wg0", 0)
	d.Exec = func(n string, a ...string) ([]byte, error) {
		calls = append(calls, n+" "+strings.Join(a, " "))
		return nil, nil
	}
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := strings.Join(calls, "\n"); !strings.Contains(got, "wg-quick up wg0") {
		t.Fatalf("无 owner 时应走 wg-quick：\n%s", got)
	}
}
