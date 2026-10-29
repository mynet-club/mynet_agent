package mynet

// PC 加速出口探测（2026-10-28 用户裁定）：
//   - PC 形态**只支持单出口**（UI/保存路径写死单选；多出口只属 GW + gnb proxy.conf）。
//   - 加速启用期间，每 15s 探测一次加速节点（出口）状况。
//
// **探测方式用驱动自身探活，不 TCP 拨 VIP:53**（2026-10-28 用户纠正）：
//   - gnb：`gnb_ctl -b <conf>/gnb.map -c -s`（经 driver.GnbPeerLinks）取出口 peer 的
//     **lastping**（`ping_ts_sec`）与 `addr4_ping_latency_usec` 时延。
//   - wg：`wg show dump` 的 **latest-handshake**。
//
// 探测归属执行器（AGENTS.md §1.2）。结果落盘，供 dns-hook status / 加速 API / 托盘读取。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/driver"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// ExitProbeInterval 出口探测周期。用户裁定 15 秒。
const ExitProbeInterval = 15 * time.Second

// exitLastPingStale gnb lastping 超过该年龄视为不新鲜（探活窗口）。
// gnb 自身 ping 周期约数十秒；15s 探测下放宽到 75s（与 domain.VPNPeer.IsActive 同窗）。
const exitLastPingStale = 75 * time.Second

// wgHandshakeFresh wg latest-handshake 有效窗口（与 parseWGDump 一致）。
const wgHandshakeFresh = 2 * time.Minute

// ExitProbeResult 一次到加速节点的探测结果（驱动探活，非主动 TCP）。
type ExitProbeResult struct {
	// NodeID 出口节点 ID（与 ProxyExit.NodeID 同形）。
	NodeID string `json:"node_id,omitempty"`
	// Target 出口 overlay VIP（展示用，可空）。
	Target string `json:"target,omitempty"`
	// Driver gnb | wireguard | none
	Driver string `json:"driver"`
	// Method gnb_map | wg_show | none
	Method string `json:"method"`
	// Link gnb 链路词（direct/relay/local/offline/unknown）；wg 用 online/offline。
	Link string `json:"link,omitempty"`
	// Reachable 驱动探活判定：gnb lastping 新鲜且链路非 offline；wg handshake 有效。
	Reachable bool `json:"reachable"`
	// RTTMs gnb addr4_ping_latency_usec；wg 无此字段则为 0。
	RTTMs float64 `json:"rtt_ms"`
	// LastPing gnb lastping（ping_ts_sec）。零值 = 未知/从未 ping 通。
	LastPing time.Time `json:"last_ping,omitempty"`
	// LastHandshake wg latest-handshake。
	LastHandshake time.Time `json:"last_handshake,omitempty"`
	// AgeMs 距 lastping/last-handshake 的毫秒数（-1 = 无时间戳）。
	AgeMs int64  `json:"age_ms"`
	Error string `json:"error,omitempty"`
	// ConsecutiveFails 连续不可达次数。
	ConsecutiveFails int       `json:"consecutive_fails"`
	At               time.Time `json:"at"`
	Probed           int       `json:"probed"`
}

var (
	exitProbeMu     sync.RWMutex
	exitProbeLatest ExitProbeResult
	exitProbeCount  int
)

// 可注入，测试不打真 gnb_ctl / wg。
var (
	gnbExitPeerLinks = driver.GnbPeerLinks
	wgExitPeerLive   = probeWGPeerLive
)

func dnsHookExitProbeFile() string {
	return filepath.Join(varDir(), "proxy", "dns-hook-exit-probe.json")
}

// currentExitProbe 出口选点：PC 单出口，取第一个 enabled exit（NodeID + Via）。
func currentExitProbe() (nodeID, via string) {
	p := EffectiveProxyPolicy()
	if p != nil {
		for _, e := range p.Exits {
			if !e.Enabled || strings.TrimSpace(e.NodeID) == "" {
				continue
			}
			return strings.TrimSpace(e.NodeID), strings.TrimSpace(e.Via)
		}
	}
	// 插件壳兜底（intent 无 exits 时）
	if base, _, err := activePluginBase(); err == nil {
		if pc, err := loadPluginAt(base, domain.PluginNameProxy); err == nil {
			if pp, err := domain.DecodePluginProxy(pc); err == nil {
				for _, e := range pp.EffectiveExits() {
					if strings.TrimSpace(e.NodeID) == "" {
						continue
					}
					return strings.TrimSpace(e.NodeID), strings.TrimSpace(e.Via)
				}
			}
		}
	}
	return "", ""
}

// probeExitOnce 用驱动探活加速节点。无出口返回 Method=none。
func probeExitOnce(nodeID, via string, now time.Time) ExitProbeResult {
	res := ExitProbeResult{
		NodeID: nodeID,
		Target: via,
		Driver: "none",
		Method: "none",
		AgeMs:  -1,
		At:     now.UTC(),
	}
	if nodeID == "" && via == "" {
		res.Error = "no exit target"
		return res
	}
	id, ok := parseExitNodeID(nodeID)
	if !ok {
		res.Error = "invalid exit node_id"
		return res
	}
	// gnb 优先：PC 加速主路径是 gnb P2；wg 用 handshake。
	if link, found := probeGnbExit(id, via, now); found {
		return link
	}
	if hs, found := probeWgExit(id, via, now); found {
		return hs
	}
	res.Error = "driver peer not found"
	return res
}

// probeGnbExit 读 gnb.map（gnb_ctl -s）取 lastping + 时延。found=false 表示无 gnb 表。
func probeGnbExit(id int64, via string, now time.Time) (ExitProbeResult, bool) {
	res := ExitProbeResult{
		NodeID: formatNodeID(id),
		Target: via,
		Driver: "gnb",
		Method: "gnb_map",
		AgeMs:  -1,
		At:     now.UTC(),
	}
	links := gnbExitPeerLinks(gnbProbeMapDir())
	if links == nil {
		return res, false
	}
	// 优先精确 nodeID；否则按 overlay VIP 认。
	pl, ok := links[id]
	if ok {
		// keep
	} else if via != "" {
		for _, p := range links {
			if p.Overlay == via {
				pl, ok = p, true
				break
			}
		}
		if ok {
			// 命中 overlay 时以表内真实 nodeID 为准（配置里的 id 可能是另一套编号）
			res.NodeID = formatNodeID(pl.NodeID)
		}
	}
	if !ok {
		return res, true // 有 gnb 表但没这个出口
	}
	res.Link = pl.Link
	res.RTTMs = pl.LatencyMs
	if !pl.LastPing.IsZero() {
		res.LastPing = pl.LastPing.UTC()
		res.AgeMs = now.Sub(pl.LastPing).Milliseconds()
	}
	res.Reachable = gnbExitReachable(pl, now)
	if !res.Reachable {
		if pl.Link == "offline" || pl.Link == "unknown" {
			res.Error = "link " + pl.Link
		} else if res.AgeMs < 0 {
			res.Error = "no lastping"
		} else {
			res.Error = "lastping stale"
		}
	}
	return res, true
}

func gnbExitReachable(pl driver.PeerLink, now time.Time) bool {
	switch pl.Link {
	case "direct", "relay", "local":
	default:
		return false
	}
	if pl.LastPing.IsZero() {
		return false
	}
	return now.Sub(pl.LastPing) <= exitLastPingStale
}

// probeWgExit wg latest-handshake。found=false 表示不是 wg / 无该 peer。
func probeWgExit(id int64, via string, now time.Time) (ExitProbeResult, bool) {
	res := ExitProbeResult{
		NodeID: formatNodeID(id),
		Target: via,
		Driver: "wireguard",
		Method: "wg_show",
		AgeMs:  -1,
		At:     now.UTC(),
	}
	live, ok := wgExitPeerLive(id)
	if !ok {
		return res, false
	}
	res.LastHandshake = live.LastHandshake.UTC()
	if !live.LastHandshake.IsZero() {
		res.AgeMs = now.Sub(live.LastHandshake).Milliseconds()
	}
	res.Reachable = live.Online
	res.Link = "offline"
	if live.Online {
		res.Link = "online"
	}
	if !res.Reachable {
		res.Error = "no recent handshake"
	}
	return res, true
}

type wgPeerLive struct {
	Online        bool
	LastHandshake time.Time
}

// probeWGPeerLive 读 wg show dump 里该节点的 handshake。ok=false = 找不到接口/peer。
func probeWGPeerLive(nodeID int64) (wgPeerLive, bool) {
	// 与 factory.DetectDriver 同思路：先 wg，再认不到就不算 wg。
	d := driver.NewWireGuardDriver("wg0", 0)
	if !d.IsAvailable() {
		return wgPeerLive{}, false
	}
	peers, err := d.GetAllPeers()
	if err != nil {
		return wgPeerLive{}, false
	}
	for _, p := range peers {
		if p.NodeID != nodeID && p.PeerID != nodeID {
			continue
		}
		return wgPeerLive{Online: p.IsOnline(), LastHandshake: p.LastHandshake}, true
	}
	return wgPeerLive{}, false
}

// gnbProbeMapDir 与 node_status 同口径：优先运行投影里的 gnb.map，否则 conf。
func gnbProbeMapDir() string {
	conf := gnbConfDir()
	if run, err := utils.ActiveRunProjectionPath(); err == nil && run != "" {
		if _, serr := os.Stat(filepath.Join(run, "gnb.map")); serr == nil {
			return run
		}
	}
	return conf
}

func parseExitNodeID(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return n, true
}

func formatNodeID(n int64) string {
	return strconv.FormatInt(n, 10)
}

// recordExitProbe 更新进程内状态并落盘。失败探测累计 consecutive_fails。
func recordExitProbe(res ExitProbeResult) {
	if res.At.IsZero() {
		res.At = time.Now().UTC()
	}
	exitProbeMu.Lock()
	prev := exitProbeLatest
	exitProbeCount++
	res.Probed = exitProbeCount
	if res.Reachable {
		res.ConsecutiveFails = 0
	} else if prev.NodeID == res.NodeID && !prev.At.IsZero() {
		res.ConsecutiveFails = prev.ConsecutiveFails + 1
	} else {
		res.ConsecutiveFails = 1
	}
	exitProbeLatest = res
	exitProbeMu.Unlock()
	saveExitProbeResult(res)
}

func saveExitProbeResult(res ExitProbeResult) {
	path := dnsHookExitProbeFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		hookLogf("exit-probe mkdir: %v", err)
		return
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		hookLogf("exit-probe write: %v", err)
		return
	}
	_ = os.Rename(tmp, path)
}

// LoadExitProbeResult 读最近一次出口探测（status / API 跨进程）。
func LoadExitProbeResult() (ExitProbeResult, bool) {
	exitProbeMu.RLock()
	inMem := exitProbeLatest
	exitProbeMu.RUnlock()
	if !inMem.At.IsZero() {
		return inMem, true
	}
	b, err := os.ReadFile(dnsHookExitProbeFile())
	if err != nil {
		return ExitProbeResult{}, false
	}
	var res ExitProbeResult
	if err := json.Unmarshal(b, &res); err != nil {
		return ExitProbeResult{}, false
	}
	return res, true
}

// resetExitProbeMemory 仅测试用：清空进程内探测缓存。
func resetExitProbeMemory() {
	exitProbeMu.Lock()
	exitProbeLatest = ExitProbeResult{}
	exitProbeCount = 0
	exitProbeMu.Unlock()
}

// ClearExitProbeResult 停用加速时清空，避免展示过期可达。
func ClearExitProbeResult() {
	resetExitProbeMemory()
	_ = os.Remove(dnsHookExitProbeFile())
}

// startExitProbeLoop 在 dns-hook worker 内启动 15s 出口探测。done 关闭后退出。
func startExitProbeLoop(done <-chan struct{}) {
	go func() {
		runExitProbeTick()
		t := time.NewTicker(ExitProbeInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				runExitProbeTick()
			}
		}
	}()
}

func runExitProbeTick() {
	nodeID, via := currentExitProbe()
	if nodeID == "" && via == "" {
		return
	}
	res := probeExitOnce(nodeID, via, time.Now())
	recordExitProbe(res)
	if !res.Reachable {
		hookLogf("exit-probe node=%s via=%s unreachable: %s", res.NodeID, res.Target, res.Error)
	}
}

// FormatExitProbeStatus 状态输出一行，供 parseDesktopHookStatus 解析。
func FormatExitProbeStatus(res ExitProbeResult, ok bool) string {
	if !ok || (res.NodeID == "" && res.Target == "") {
		return "exit_probe: node= target= driver=none method=none reachable=false rtt_ms=0 age_ms=-1 fails=0"
	}
	age := res.AgeMs
	return "exit_probe: node=" + res.NodeID +
		" target=" + res.Target +
		" driver=" + res.Driver +
		" method=" + res.Method +
		" link=" + res.Link +
		" reachable=" + strconv.FormatBool(res.Reachable) +
		" rtt_ms=" + strconv.FormatFloat(res.RTTMs, 'f', 1, 64) +
		" age_ms=" + strconv.FormatInt(age, 10) +
		" fails=" + strconv.Itoa(res.ConsecutiveFails)
}
