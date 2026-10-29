package mynet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/driver"
)

func setupExitProbeHome(t *testing.T) {
	t.Helper()
	t.Setenv("MYNET_HOME", t.TempDir())
	t.Setenv("MYNET_DATA_HOME", "")
	t.Setenv("MYNET_CONFIG_HOME", "")
	resetExitProbeMemory()
	// 默认不打真 gnb_ctl / wg
	gnbExitPeerLinks = func(string) map[int64]driver.PeerLink { return nil }
	wgExitPeerLive = func(int64) (wgPeerLive, bool) { return wgPeerLive{}, false }
	t.Cleanup(func() {
		resetExitProbeMemory()
		gnbExitPeerLinks = driver.GnbPeerLinks
		wgExitPeerLive = probeWGPeerLive
		_ = os.Remove(dnsHookExitProbeFile())
	})
}

func TestProbeExitOnceEmpty(t *testing.T) {
	setupExitProbeHome(t)
	res := probeExitOnce("", "", time.Now())
	if res.Reachable {
		t.Fatal("空出口不得判可达")
	}
	if res.Method != "none" {
		t.Fatalf("method=%q want none", res.Method)
	}
}

func TestProbeGnbExitUsesLastPing(t *testing.T) {
	setupExitProbeHome(t)
	now := time.Unix(1700000100, 0)
	last := now.Add(-10 * time.Second)
	gnbExitPeerLinks = func(string) map[int64]driver.PeerLink {
		return map[int64]driver.PeerLink{
			1001: {NodeID: 1001, Overlay: "10.0.0.8", Link: "direct", LatencyMs: 1.5, LastPing: last},
		}
	}
	res := probeExitOnce("1001", "10.0.0.8", now)
	if res.Method != "gnb_map" || res.Driver != "gnb" {
		t.Fatalf("driver=%s method=%s", res.Driver, res.Method)
	}
	if !res.Reachable {
		t.Fatalf("新鲜 lastping 应可达: %+v", res)
	}
	if res.RTTMs != 1.5 {
		t.Fatalf("rtt=%v want 1.5", res.RTTMs)
	}
	if res.LastPing.Unix() != last.Unix() {
		t.Fatalf("lastping=%v want %v", res.LastPing, last)
	}
	if res.AgeMs != 10000 {
		t.Fatalf("age_ms=%d want 10000", res.AgeMs)
	}
}

func TestProbeGnbExitStaleLastPingUnreachable(t *testing.T) {
	setupExitProbeHome(t)
	now := time.Unix(1700000100, 0)
	gnbExitPeerLinks = func(string) map[int64]driver.PeerLink {
		return map[int64]driver.PeerLink{
			1001: {NodeID: 1001, Link: "direct", LastPing: now.Add(-5 * time.Minute)},
		}
	}
	res := probeExitOnce("1001", "", now)
	if res.Reachable {
		t.Fatalf("过期 lastping 不得判可达: %+v", res)
	}
	if res.Error == "" {
		t.Fatal("应写明不可达原因")
	}
}

func TestProbeGnbExitOfflineUnreachable(t *testing.T) {
	setupExitProbeHome(t)
	now := time.Unix(1700000100, 0)
	gnbExitPeerLinks = func(string) map[int64]driver.PeerLink {
		return map[int64]driver.PeerLink{
			1001: {NodeID: 1001, Link: "offline", LastPing: now.Add(-2 * time.Second)},
		}
	}
	res := probeExitOnce("1001", "", now)
	if res.Reachable {
		t.Fatal("offline 链路不得判可达")
	}
	if !strings.Contains(res.Error, "offline") {
		t.Fatalf("error=%q", res.Error)
	}
}

func TestProbeGnbExitMatchByOverlay(t *testing.T) {
	setupExitProbeHome(t)
	now := time.Unix(1700000100, 0)
	last := now.Add(-3 * time.Second)
	gnbExitPeerLinks = func(string) map[int64]driver.PeerLink {
		return map[int64]driver.PeerLink{
			42: {NodeID: 42, Overlay: "10.9.9.9", Link: "relay", LastPing: last},
		}
	}
	// nodeID 对不上时按 via 认
	res := probeExitOnce("999", "10.9.9.9", now)
	if !res.Reachable || res.NodeID != "42" {
		t.Fatalf("应按 overlay 认出 42: %+v", res)
	}
}

func TestProbeWgExitHandshake(t *testing.T) {
	setupExitProbeHome(t)
	now := time.Unix(1700000100, 0)
	// gnb 表空 → 落到 wg
	hs := now.Add(-30 * time.Second)
	wgExitPeerLive = func(id int64) (wgPeerLive, bool) {
		if id != 77 {
			return wgPeerLive{}, false
		}
		return wgPeerLive{Online: true, LastHandshake: hs}, true
	}
	res := probeExitOnce("77", "10.1.1.1", now)
	if res.Method != "wg_show" || res.Driver != "wireguard" {
		t.Fatalf("driver=%s method=%s", res.Driver, res.Method)
	}
	if !res.Reachable {
		t.Fatalf("新鲜 handshake 应可达: %+v", res)
	}
	if res.LastHandshake.Unix() != hs.Unix() {
		t.Fatalf("last_handshake=%v", res.LastHandshake)
	}
}

func TestProbeWgExitNoHandshake(t *testing.T) {
	setupExitProbeHome(t)
	now := time.Unix(1700000100, 0)
	wgExitPeerLive = func(int64) (wgPeerLive, bool) {
		return wgPeerLive{Online: false, LastHandshake: time.Time{}}, true
	}
	res := probeExitOnce("77", "", now)
	if res.Reachable {
		t.Fatal("无 handshake 不得判可达")
	}
}

func TestProbeExitNoDriverPeer(t *testing.T) {
	setupExitProbeHome(t)
	res := probeExitOnce("55", "10.2.2.2", time.Now())
	if res.Reachable {
		t.Fatal("找不到 peer 不得判可达")
	}
	if res.Error == "" {
		t.Fatal("应有错误说明")
	}
}

func TestExitProbeConsecutiveFailsAccumulate(t *testing.T) {
	setupExitProbeHome(t)
	base := time.Unix(1700000000, 0)
	recordExitProbe(ExitProbeResult{NodeID: "9", Reachable: false, Error: "x", At: base})
	recordExitProbe(ExitProbeResult{NodeID: "9", Reachable: false, Error: "x", At: base.Add(time.Second)})
	got, ok := LoadExitProbeResult()
	if !ok {
		t.Fatal("应有结果")
	}
	if got.ConsecutiveFails != 2 {
		t.Fatalf("fails=%d want 2", got.ConsecutiveFails)
	}
	recordExitProbe(ExitProbeResult{NodeID: "9", Reachable: true, At: base.Add(2 * time.Second)})
	got, _ = LoadExitProbeResult()
	if got.ConsecutiveFails != 0 {
		t.Fatalf("成功后 fails 应清零，得到 %d", got.ConsecutiveFails)
	}
}

func TestLoadExitProbeResultFromDisk(t *testing.T) {
	setupExitProbeHome(t)
	path := dnsHookExitProbeFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := ExitProbeResult{NodeID: "8", Target: "10.1.2.3", Driver: "gnb", Method: "gnb_map", Reachable: true, RTTMs: 12.5, At: time.Now().UTC()}
	b, _ := json.Marshal(payload)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	resetExitProbeMemory()
	got, ok := LoadExitProbeResult()
	if !ok {
		t.Fatal("应从磁盘读到")
	}
	if got.NodeID != "8" || !got.Reachable {
		t.Fatalf("got=%+v", got)
	}
}

func TestFormatExitProbeStatusEmpty(t *testing.T) {
	setupExitProbeHome(t)
	line := FormatExitProbeStatus(ExitProbeResult{}, false)
	if !strings.Contains(line, "reachable=false") || !strings.Contains(line, "method=none") {
		t.Fatalf("空状态行形状不对: %s", line)
	}
}

func TestFormatExitProbeStatusGnb(t *testing.T) {
	setupExitProbeHome(t)
	res := ExitProbeResult{
		NodeID: "1001", Target: "10.0.0.8", Driver: "gnb", Method: "gnb_map",
		Link: "direct", Reachable: true, RTTMs: 1.5, AgeMs: 200,
	}
	line := FormatExitProbeStatus(res, true)
	for _, want := range []string{
		"node=1001", "target=10.0.0.8", "driver=gnb", "method=gnb_map",
		"link=direct", "reachable=true", "rtt_ms=1.5", "age_ms=200",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("状态行缺 %q: %s", want, line)
		}
	}
}
