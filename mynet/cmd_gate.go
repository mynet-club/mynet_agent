package mynet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mynet-club/mynet_agent/mynet/upgrade"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// cmdGate — P7 再部署门禁（G-subject / G-route / G-fw / G-sync / G-update）。
//
//	mynet gate [--json] [--only subject|route|fw|sync|update]
//
// 五闸全绿才允许再部署。单闸失败退出码 1，用法错误 2。
// 「测不了」不算红（与 route doctor / firewall doctor 同口径），
// 但会在报告里标 skip 并给出原因——缺依赖必须说清，不能装成「通过」。
func cmdGate(args []string) int {
	asJSON := false
	only := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--only":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "gate: --only 需要闸名")
				return 2
			}
			only = args[i+1]
			i++
		default:
			fmt.Fprintln(os.Stderr, "用法: mynet gate [--json] [--only subject|route|fw|sync|update]")
			return 2
		}
	}

	gates := []gateResult{
		runGateSubject(),
		runGateRoute(),
		runGateFW(),
		runGateSync(),
		runGateUpdate(),
	}
	if only != "" {
		var filtered []gateResult
		for _, g := range gates {
			if g.Name == only {
				filtered = append(filtered, g)
			}
		}
		if len(filtered) == 0 {
			fmt.Fprintf(os.Stderr, "gate: 未知闸名 %q（subject|route|fw|sync|update）\n", only)
			return 2
		}
		gates = filtered
	}

	rep := gateReport{Gates: gates}
	pass, fail, skip := 0, 0, 0
	for _, g := range gates {
		switch g.Status {
		case "pass":
			pass++
		case "fail":
			fail++
		default:
			skip++
		}
	}
	rep.Summary = fmt.Sprintf("%d pass / %d fail / %d skip", pass, fail, skip)

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		printGateReport(rep)
	}
	if fail > 0 {
		return 1
	}
	return 0
}

type gateResult struct {
	Name   string   `json:"name"`
	Title  string   `json:"title"`
	Status string   `json:"status"` // pass | fail | skip
	Detail string   `json:"detail,omitempty"`
	Notes  []string `json:"notes,omitempty"`
}

type gateReport struct {
	Gates   []gateResult `json:"gates"`
	Summary string       `json:"summary"`
}

// runGateSubject — G-subject：主体可解析、进程锚一致。
func runGateSubject() gateResult {
	g := gateResult{Name: "subject", Title: "G-subject 主体定位"}
	subj, err := utils.ResolveSubject(utils.ResolveOpts{})
	if err != nil {
		g.Status = "fail"
		g.Detail = err.Error()
		return g
	}
	if subj.NodeID == "" || subj.ConfDir == "" {
		g.Status = "fail"
		g.Detail = "主体缺 node_id 或 conf_dir"
		return g
	}
	g.Status = "pass"
	g.Detail = fmt.Sprintf("node_id=%s conf=%s source=%s", subj.NodeID, subj.ConfDir, subj.Source)
	if len(subj.ToolsMissing) > 0 {
		g.Notes = append(g.Notes, fmt.Sprintf("工具缺失 %v", subj.ToolsMissing))
	}
	return g
}

// runGateRoute — G-route：route doctor 可跑且无「不在隧道」红灯。
func runGateRoute() gateResult {
	g := gateResult{Name: "route", Title: "G-route 路由/回程"}
	subj, err := utils.ResolveSubject(utils.ResolveOpts{})
	if err != nil || subj == nil || subj.ConfDir == "" {
		g.Status = "skip"
		g.Detail = "无主体 conf，无法取 peer 列表"
		return g
	}
	// route.conf 可能是 root 600 —— 读不到必须说清是权限，不能装成「无 peer」
	routeConf := filepath.Join(subj.ConfDir, "route.conf")
	if _, rerr := os.Stat(routeConf); rerr != nil {
		g.Status = "skip"
		g.Detail = "route.conf 不可读: " + rerr.Error()
		return g
	}
	peers := peersFromRouteConf(subj.ConfDir)
	if len(peers) == 0 {
		// 文件存在但解析出 0 个 /32：区分「权限」与「真空」
		if b, rerr := os.ReadFile(routeConf); rerr != nil {
			g.Status = "skip"
			g.Detail = "route.conf 读失败（权限？）: " + rerr.Error()
			return g
		} else if len(b) > 0 {
			g.Status = "skip"
			g.Detail = "route.conf 无 /32 peer（只有网段或本机）"
			return g
		}
		g.Status = "skip"
		g.Detail = "route.conf 为空"
		return g
	}
	offTunnel := 0
	for _, vip := range peers {
		if subj.NodeID != "" && vip == confOwnVIP(subj.ConfDir) {
			continue
		}
		_, dev, _ := routeGet(vip)
		if !isTunnelDev(dev) {
			offTunnel++
			g.Notes = append(g.Notes, fmt.Sprintf("%s → dev %q 不是隧道口", vip, dev))
		}
	}
	if offTunnel > 0 {
		g.Status = "fail"
		g.Detail = fmt.Sprintf("%d/%d peer 不在隧道", offTunnel, len(peers))
	} else {
		g.Status = "pass"
		g.Detail = fmt.Sprintf("%d peer 全在隧道", len(peers))
	}
	return g
}

// runGateFW — G-fw：firewall doctor 机内层放行（测不了标 skip）。
func runGateFW() gateResult {
	g := gateResult{Name: "fw", Title: "G-fw 防火墙两层"}
	port := 9016
	if subj, err := utils.ResolveSubject(utils.ResolveOpts{}); err == nil && subj != nil && subj.ConfDir != "" {
		if p := confListenPort(subj.ConfDir); p > 0 {
			port = p
		}
	}
	m := checkMachinePort(port)
	switch {
	case m.Backend == "none" || m.Backend == "pf":
		g.Status = "skip"
		g.Detail = fmt.Sprintf("机内层测不了（%s）", m.Backend)
		g.Notes = m.Notes
	case m.Accepted:
		g.Status = "pass"
		g.Detail = fmt.Sprintf("UDP %d 已放行（%s）", port, m.Backend)
	default:
		g.Status = "fail"
		g.Detail = fmt.Sprintf("UDP %d 未放行（%s）", port, m.Backend)
		g.Notes = m.Notes
	}
	g.Notes = append(g.Notes, "云侧安全组需人工核对（本机看不到）")
	return g
}

// runGateSync — G-sync：parity critical 项全一致。
func runGateSync() gateResult {
	g := gateResult{Name: "sync", Title: "G-sync 对等配置 parity"}
	items := currentParity()
	if len(items) == 0 {
		g.Status = "skip"
		g.Detail = "无可比对等项（缺 remote 或本地 node.conf）"
		return g
	}
	bad := 0
	for _, it := range items {
		if it.Critical && !it.Match {
			bad++
			g.Notes = append(g.Notes, fmt.Sprintf("critical %s local=%s remote=%s", it.Key, it.Local, it.Remote))
		}
	}
	if bad > 0 {
		g.Status = "fail"
		g.Detail = fmt.Sprintf("%d 个 critical 不一致", bad)
	} else {
		g.Status = "pass"
		g.Detail = fmt.Sprintf("%d 项 parity 无 critical 冲突", len(items))
	}
	return g
}

// runGateUpdate — G-update：索引可达 + 能挑出目标版本（不执行安装）。
func runGateUpdate() gateResult {
	g := gateResult{Name: "update", Title: "G-update 升级链路"}
	index := "https://ctl.mynet.club"
	if v := os.Getenv("MYNET_APPS_INDEX"); v != "" {
		index = v
	}
	svc := upgrade.NewService(index)
	all, err := svc.Fetch(index)
	if err != nil {
		// 索引不可达属环境问题：开发机离线 / ctl 未部署时标 skip，不挡本地再部署
		g.Status = "skip"
		g.Detail = "索引不可达: " + err.Error()
		return g
	}
	best := pickLatestMynet(all)
	if best == nil {
		g.Status = "fail"
		g.Detail = "索引中无 mynet_agent 组件"
		return g
	}
	cmp := upgrade.CompareVersions(best.Version, Version)
	g.Status = "pass"
	g.Detail = fmt.Sprintf("cur=%s latest=%s（cmp=%d）", Version, best.Version, cmp)
	if best.Checksum == "" {
		g.Notes = append(g.Notes, "产物缺 sha256 —— 真升级时会被拒绝")
	}
	return g
}

func printGateReport(rep gateReport) {
	fmt.Println("mynet gate（P7 再部署门禁）")
	for _, g := range rep.Gates {
		mark := map[string]string{"pass": "✓", "fail": "✗", "skip": "·"}[g.Status]
		fmt.Printf("  %s %-8s %-22s %s\n", mark, g.Name, g.Title, g.Detail)
		for _, n := range g.Notes {
			fmt.Println("      -", n)
		}
	}
	fmt.Println("总结:", rep.Summary)
	if len(rep.Summary) > 0 && rep.Summary[0] != '0' {
		// 形如 "N pass / M fail / K skip"；M>0 时给出明确禁令
	}
	for _, g := range rep.Gates {
		if g.Status == "fail" {
			fmt.Println("有红灯 —— 不得再部署")
			return
		}
	}
}
