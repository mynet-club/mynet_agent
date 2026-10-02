package mynet

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// cmdRouteDoctor — N4：连通性机测子集（troubleshoot-connectivity.md §9）。
//
//	mynet route doctor [--json] [--peer VIP]...
//
// 只出数据，不解读根因：
//
//	D 步  ip route get / route get → dev 是否隧道口
//	回程  需对端 shell 时打印「请在对端跑同命令」
//	计数  gnb_ctl -s 的 in/out（若有同构建 ctl）
//	对等  node.conf 的 zip/crypto/passcode 摘要
//
// 人工判断见手册；归属见 route-firewall-split.md。
func cmdRouteDoctor(args []string) int {
	asJSON := false
	var peers []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--peer":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "route doctor: --peer 需要 VIP")
				return 2
			}
			peers = append(peers, args[i+1])
			i++
		default:
			peers = append(peers, args[i])
		}
	}

	// 主体（进程锚）
	subj, subjErr := utils.ResolveSubject(utils.ResolveOpts{})

	rep := routeDoctorReport{
		Subject: subj,
	}
	if subjErr != nil {
		rep.Warnings = append(rep.Warnings, "subject: "+subjErr.Error())
	}
	if subj != nil {
		if len(subj.ToolsMissing) > 0 {
			rep.Warnings = append(rep.Warnings,
				fmt.Sprintf("工具不完整 missing=%v —— 跳过 gnb_ctl 计数", subj.ToolsMissing))
		}
	}

	// 对等项摘要
	if subj != nil && subj.ConfDir != "" {
		rep.Parity = readParityBrief(subj.ConfDir)
	}

	// peer 列表：显式 > route.conf > mode.overlay
	if len(peers) == 0 && subj != nil && subj.ConfDir != "" {
		peers = peersFromRouteConf(subj.ConfDir)
	}

	for _, vip := range peers {
		// 本节点自己的 VIP 不查
		if subj != nil && strings.HasPrefix(subj.ConfDir, vip) {
			continue
		}
		if subj != nil && subj.NodeID != "" && vip == confOwnVIP(subj.ConfDir) {
			continue
		}
		r := peerRoute{VIP: vip}
		r.RouteGet, r.Device, r.Via = routeGet(vip)
		r.InTunnel = isTunnelDev(r.Device)
		if !r.InTunnel {
			r.Notes = append(r.Notes, "D: dev 不是隧道口 —— 见手册 §4（apply --auto / 回程）")
		}
		rep.Peers = append(rep.Peers, r)
	}

	// gnb 计数
	if subj != nil && subj.GnbCtl != "" && subj.MapPath != "" {
		rep.NodeCounters = gnbCounters(subj.GnbCtl, subj.MapPath)
	} else if subj != nil && subj.ConfDir != "" {
		// 同目录没有 ctl 时给出提示，不捡异构建
		rep.Warnings = append(rep.Warnings,
			"无同目录 gnb_ctl，跳过 in/out 计数（禁止使用异构建 ctl）")
	}

	// 回程提示
	if len(rep.Peers) > 0 {
		rep.Warnings = append(rep.Warnings,
			"回程需在对端跑同命令：ip route get <本机VIP>（手册 §4.4）")
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		printRouteDoctor(rep)
	}

	// 退出码：任一对不在隧道 → 1（可机测的红灯）
	for _, p := range rep.Peers {
		if !p.InTunnel {
			return 1
		}
	}
	if len(rep.Warnings) > 0 && len(rep.Peers) == 0 {
		return 1
	}
	return 0
}

type routeDoctorReport struct {
	Subject      *utils.Subject    `json:"subject,omitempty"`
	Peers        []peerRoute       `json:"peers,omitempty"`
	Parity       map[string]string `json:"parity,omitempty"`
	NodeCounters []nodeCounter     `json:"node_counters,omitempty"`
	Warnings     []string          `json:"warnings,omitempty"`
}

type peerRoute struct {
	VIP      string   `json:"vip"`
	RouteGet string   `json:"route_get"`
	Device   string   `json:"device"`
	Via      string   `json:"via,omitempty"`
	InTunnel bool     `json:"in_tunnel"`
	Notes    []string `json:"notes,omitempty"`
}

type nodeCounter struct {
	NodeID  string `json:"node_id"`
	TunIPv4 string `json:"tun_ipv4"`
	Path    string `json:"path"`
	In      string `json:"in"`
	Out     string `json:"out"`
}

func printRouteDoctor(rep routeDoctorReport) {
	fmt.Println("route doctor（机测子集，判读见 troubleshoot-connectivity.md）")
	if rep.Subject != nil {
		fmt.Printf("主体: conf=%s source=%s\n", rep.Subject.ConfDir, rep.Subject.Source)
	}
	if len(rep.Parity) > 0 {
		fmt.Printf("对等: %v\n", rep.Parity)
	}
	if len(rep.Peers) == 0 {
		fmt.Println("（无 peer：提供 --peer VIP 或保证 route.conf 可读）")
	}
	for _, p := range rep.Peers {
		mark := "✓"
		if !p.InTunnel {
			mark = "✗"
		}
		fmt.Printf("  %s %s → dev=%s via=%s\n", mark, p.VIP, p.Device, p.Via)
		for _, n := range p.Notes {
			fmt.Printf("      %s\n", n)
		}
	}
	if len(rep.NodeCounters) > 0 {
		fmt.Println("gnb 计数:")
		for _, c := range rep.NodeCounters {
			fmt.Printf("  %s %s path=%s in=%s out=%s\n", c.NodeID, c.TunIPv4, c.Path, c.In, c.Out)
		}
	}
	for _, w := range rep.Warnings {
		fmt.Printf("  ⚠ %s\n", w)
	}
}

func isTunnelDev(dev string) bool {
	d := strings.ToLower(dev)
	if d == "" {
		return false
	}
	for _, p := range []string{"gnb_tun", "gnb", "wg", "utun", "tun"} {
		if strings.HasPrefix(d, p) {
			return true
		}
	}
	return false
}

// routeGet 查系统路由。返回原始输出、dev、via。
func routeGet(vip string) (raw, dev, via string) {
	var out []byte
	var err error
	switch runtime.GOOS {
	case "darwin":
		out, err = exec.Command("route", "-n", "get", vip).Output()
	case "windows":
		out, err = exec.Command("powershell", "-NoProfile", "-Command",
			fmt.Sprintf("(Find-NetRoute -RemoteIPAddress %s -ErrorAction SilentlyContinue | Select-Object -First 1).InterfaceAlias", vip)).Output()
	default:
		out, err = exec.Command("ip", "route", "get", vip).Output()
	}
	if err != nil {
		return "", "", ""
	}
	raw = strings.TrimSpace(string(out))
	dev, via = parseRouteGet(raw, runtime.GOOS)
	return raw, dev, via
}

func parseRouteGet(raw, goos string) (dev, via string) {
	if goos == "windows" {
		return strings.TrimSpace(raw), ""
	}
	if goos == "darwin" {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "interface:") {
				dev = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
			}
			if strings.HasPrefix(line, "gateway:") {
				via = strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
			}
		}
		return
	}
	// Linux: 10.x via 172.26.0.1 dev ens5 src ...
	fields := strings.Fields(raw)
	for i := 0; i < len(fields); i++ {
		if fields[i] == "dev" && i+1 < len(fields) {
			dev = fields[i+1]
		}
		if fields[i] == "via" && i+1 < len(fields) {
			via = fields[i+1]
		}
	}
	return
}

func peersFromRouteConf(confDir string) []string {
	b, err := os.ReadFile(filepath.Join(confDir, "route.conf"))
	if err != nil {
		return nil
	}
	var peers []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "|")
		// <uuid>|<ip>|<dotted mask>
		if len(parts) < 3 {
			continue
		}
		ip, mask := parts[1], parts[2]
		// 只把 /32 VIP 当互通对象；网段（如 192.168.0.0/24）可能是本机直连，
		// 不该红灯。本节点自己的 VIP 也不查。
		if mask != "255.255.255.255" {
			continue
		}
		if strings.Count(ip, ".") != 3 {
			continue
		}
		peers = append(peers, ip)
	}
	return dedupStrings(peers)
}

func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// confOwnVIP 从 route.conf 找「行里 mask=/32 且 uuid 行在 address/node 里是自己」——
// 简化：读 node.conf 的 nodeid，再找 route.conf 里同 uuid 的 /32。
func confOwnVIP(confDir string) string {
	nb, err := os.ReadFile(filepath.Join(confDir, "node.conf"))
	if err != nil {
		return ""
	}
	nodeID := ""
	for _, line := range strings.Split(string(nb), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nodeid ") {
			nodeID = strings.TrimSpace(strings.TrimPrefix(line, "nodeid "))
			break
		}
	}
	if nodeID == "" {
		return ""
	}
	rb, err := os.ReadFile(filepath.Join(confDir, "route.conf"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(rb), "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) >= 3 && parts[0] == nodeID && parts[2] == "255.255.255.255" {
			return parts[1]
		}
	}
	return ""
}

func readParityBrief(confDir string) map[string]string {
	b, err := os.ReadFile(filepath.Join(confDir, "node.conf"))
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "zip", "zip-level", "crypto", "passcode", "safe-index", "multi-socket":
			m[f[0]] = strings.Join(f[1:], " ")
		}
	}
	return m
}

func gnbCounters(ctl, mapPath string) []nodeCounter {
	out, err := exec.Command(ctl, "-b", mapPath, "-s").Output()
	if err != nil {
		return nil
	}
	var rows []nodeCounter
	var cur *nodeCounter
	flush := func() {
		if cur != nil && cur.NodeID != "" {
			rows = append(rows, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "node "):
			flush()
			cur = &nodeCounter{NodeID: strings.TrimSpace(strings.TrimPrefix(line, "node "))}
		case strings.HasPrefix(line, "tun_ipv4") && cur != nil:
			cur.TunIPv4 = strings.TrimSpace(strings.TrimPrefix(line, "tun_ipv4"))
		case strings.HasPrefix(line, "ipv4 ") && cur != nil:
			cur.Path = strings.TrimSpace(strings.TrimPrefix(line, "ipv4 "))
		case strings.HasPrefix(line, "in ") && cur != nil:
			cur.In = compactBytes(line)
		case strings.HasPrefix(line, "out ") && cur != nil:
			cur.Out = compactBytes(line)
		}
	}
	flush()
	return rows
}

// compactBytes "in  48834 (47.689K bytes)" → "48834"
func compactBytes(line string) string {
	f := strings.Fields(line)
	if len(f) < 2 {
		return line
	}
	if _, err := strconv.Atoi(f[1]); err == nil {
		return f[1]
	}
	return f[1]
}
