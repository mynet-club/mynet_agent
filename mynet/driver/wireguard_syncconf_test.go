package driver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// StripWGQuickKeys 必须剥掉 wg-quick 专有键（否则 `wg syncconf` 语法错），
// 但**不能**剥 wg 自己的键（FwMark 尤其容易被误伤）。
func TestStripWGQuickKeys(t *testing.T) {
	in := `[Interface]
PrivateKey = priv
ListenPort = 51820
Table = off
Address = 10.9.0.1/24
MTU = 1420
DNS = 1.1.1.1
PreUp = echo hi
FwMark = 0xc8

# 注释保留
[Peer]
PublicKey = pk
AllowedIPs = 10.9.0.2/32
Endpoint = 1.2.3.4:51820
PersistentKeepalive = 25
`
	got := StripWGQuickKeys(in)
	for _, bad := range []string{"Table", "Address", "MTU", "DNS", "PreUp"} {
		if strings.Contains(got, bad) {
			t.Fatalf("wg-quick 专有键 %s 应被剥掉:\n%s", bad, got)
		}
	}
	for _, keep := range []string{
		"[Interface]", "[Peer]", "PrivateKey", "ListenPort", "FwMark",
		"PublicKey", "AllowedIPs", "Endpoint", "PersistentKeepalive", "# 注释保留",
	} {
		if !strings.Contains(got, keep) {
			t.Fatalf("wg 自己的键/段 %s 不该被剥:\n%s", keep, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("结果应以换行收尾（syncconf 逐行读）:\n%q", got)
	}
}

// 接口在跑 → Reload 走 `wg syncconf`，**不碰 wg-quick**（不断链路）。
func TestWGDriverReloadUsesSyncconfWhenUp(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "wg0.conf")
	if err := os.WriteFile(conf, []byte("[Interface]\nPrivateKey = p\nTable = off\n\n[Peer]\nPublicKey = k\nAllowedIPs = 10.9.0.2/32\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	d := NewWireGuardDriver("wg0", 0)
	d.ConfPath = conf
	d.Exec = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, nil // 全成功
	}
	if err := d.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	got := strings.Join(calls, "\n")
	if !strings.Contains(got, "wg syncconf wg0 ") {
		t.Fatalf("应走 wg syncconf：\n%s", got)
	}
	if strings.Contains(got, "wg-quick") {
		t.Fatalf("热路径不该动 wg-quick（那会断链路）：\n%s", got)
	}
}

// 接口不在 → 回落冷启（契约：HotReload 下 Reload 仍须把状态带到位）。
func TestWGDriverReloadFallsBackColdWhenDown(t *testing.T) {
	var calls []string
	d := NewWireGuardDriver("wg0", 0)
	d.ConfPath = filepath.Join(t.TempDir(), "missing.conf")
	d.Exec = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "wg" {
			return nil, errors.New("Unable to access interface: No such device")
		}
		return nil, nil
	}
	if err := d.Reload(); err != nil {
		t.Fatalf("Reload 应回落成功: %v", err)
	}
	got := strings.Join(calls, "\n")
	for _, want := range []string{"wg-quick down wg0", "wg-quick up wg0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("接口不在时应回落冷启，缺 %q：\n%s", want, got)
		}
	}
}

// 热同步失败（配置错/剥错）也要回落，而不是把错报出去中断 reload。
func TestWGDriverReloadFallsBackWhenSyncconfFails(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "wg0.conf")
	if err := os.WriteFile(conf, []byte("[Interface]\nPrivateKey = p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	d := NewWireGuardDriver("wg0", 0)
	d.ConfPath = conf
	d.Exec = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "wg" && len(args) > 0 && args[0] == "syncconf" {
			return []byte("Line unrecognized: `Bad'"), errors.New("exit status 1")
		}
		return nil, nil
	}
	if err := d.Reload(); err != nil {
		t.Fatalf("syncconf 失败应回落冷启: %v", err)
	}
	got := strings.Join(calls, "\n")
	for _, want := range []string{"wg syncconf wg0 ", "wg-quick down wg0", "wg-quick up wg0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q：\n%s", want, got)
		}
	}
}

// 地址只进 bring-up（wg-quick / init 的 ip+wg 兜底都读 `Address=`），**不进** hot sync
// （`wg setconf/syncconf` 不认这个键，R7 的 crypto/network 分层）。
func TestRenderWGConfigEmitsAddressOnlyForBringUp(t *testing.T) {
	w := &domain.WireGuardIntent{
		Interface: domain.WgInterface{PrivateKey: "k", ListenPort: 51820},
		Peers:     []domain.WgPeer{{PublicKey: "p", AllowedIPs: []string{"10.0.0.2/32"}}},
	}
	text := RenderWGConfig(w, []string{"10.9.0.1/24", "fd00::1/64"})
	if !strings.Contains(text, "Address = 10.9.0.1/24, fd00::1/64") {
		t.Fatalf("应把地址写进 [Interface]：\n%s", text)
	}
	if strings.Contains(StripWGQuickKeys(text), "Address") {
		t.Fatalf("syncconf 形态不该带 Address（wg 不认）：\n%s", StripWGQuickKeys(text))
	}
	if strings.Contains(RenderWGConfig(w, nil), "Address") {
		t.Fatal("没有地址时不该凭空写 Address 行")
	}
}
