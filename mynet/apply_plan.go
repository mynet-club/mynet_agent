package mynet

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/mynet-club/mynet_agent-V2/shared"
)

// ApplyPlan 是 L6 写入计划：只含系统网络语义，不含驱动原生语法。
// 跨驱动互通（A gnb → C 双驱动 → B wg）就是这里的几条 Route，驱动层互不知晓对方。
type ApplyPlan struct {
	Routes     []RouteOp    `json:"routes,omitempty"`
	Firewall   []FirewallOp `json:"firewall,omitempty"`
	Interfaces []IfaceOp    `json:"interfaces,omitempty"`
	ExtraExec  []ExecOp     `json:"extra_exec,omitempty"` // 兜底；优先结构化字段
}

type RouteOp struct {
	Destination string `json:"destination"` // 10.8.0.0/24
	Gateway     string `json:"gateway,omitempty"`
	Device      string `json:"device"` // gnb0 / wg0 —— 对 apply 无差别
	Metric      int    `json:"metric,omitempty"`
	Action      string `json:"action"` // add | replace | del
}

type FirewallOp struct {
	Table   string `json:"table,omitempty"`
	Chain   string `json:"chain,omitempty"`
	Action  string `json:"action"` // ensure-masq | open-port | …
	Device  string `json:"device,omitempty"`
	Port    int    `json:"port,omitempty"`
	Proto   string `json:"proto,omitempty"`
	Comment string `json:"comment,omitempty"`
}

type IfaceOp struct {
	Name   string `json:"name"`
	Action string `json:"action"` // up | down
	MTU    int    `json:"mtu,omitempty"`
}

type ExecOp struct {
	// GUARD-OK 逃生舱：仅当结构化字段无法表达时使用；优先改计划结构
	Argv []string `json:"argv"`
}

func readApplyPlan(path string) (*ApplyPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p ApplyPlan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// executeApply 按 system.json 能力选择工具执行。唯一写入入口（F-5）。
func executeApply(snap *shared.SystemSnapshot, plan *ApplyPlan, dry bool) int {
	fail := 0
	for _, r := range plan.Routes {
		argv := routeArgv(snap, r)
		if err := runOp(dry, argv); err != nil {
			fmt.Fprintf(os.Stderr, "route %s %s: %v\n", r.Action, r.Destination, err)
			fail = 1
		}
	}
	for _, f := range plan.Firewall {
		argv := firewallArgv(snap, f)
		if len(argv) == 0 {
			fmt.Fprintf(os.Stderr, "firewall %s: 当前 system.json 无可用工具\n", f.Action)
			fail = 1
			continue
		}
		if err := runOp(dry, argv); err != nil {
			fmt.Fprintf(os.Stderr, "firewall %s: %v\n", f.Action, err)
			fail = 1
		}
	}
	for _, i := range plan.Interfaces {
		argv := ifaceArgv(i)
		if err := runOp(dry, argv); err != nil {
			fmt.Fprintf(os.Stderr, "iface %s %s: %v\n", i.Action, i.Name, err)
			fail = 1
		}
	}
	for _, e := range plan.ExtraExec {
		if err := runOp(dry, e.Argv); err != nil {
			fmt.Fprintf(os.Stderr, "exec %v: %v\n", e.Argv, err)
			fail = 1
		}
	}
	return fail
}

func routeArgv(snap *shared.SystemSnapshot, r RouteOp) []string {
	// 统一用 `ip route`（linux/openwrt/darwin-with-net-tools）
	verb := map[string]string{"add": "add", "replace": "replace", "del": "del"}[r.Action]
	if verb == "" {
		verb = "replace"
	}
	argv := []string{"ip", "route", verb, r.Destination}
	if r.Gateway != "" {
		argv = append(argv, "via", r.Gateway)
	}
	if r.Device != "" {
		argv = append(argv, "dev", r.Device)
	}
	if r.Metric > 0 {
		argv = append(argv, "metric", fmt.Sprintf("%d", r.Metric))
	}
	_ = snap
	return argv
}

func firewallArgv(snap *shared.SystemSnapshot, f FirewallOp) []string {
	switch f.Action {
	case "ensure-masq":
		if snap.Capabilities["nft"] {
			return []string{"nft", "add", "rule", "ip", "nat", "POSTROUTING",
				"oifname", f.Device, "masquerade"}
		}
		if snap.Capabilities["iptables"] {
			return []string{"iptables", "-t", "nat", "-A", "POSTROUTING",
				"-o", f.Device, "-j", "MASQUERADE"}
		}
		return nil
	case "open-port":
		if snap.Capabilities["iptables"] {
			argv := []string{"iptables", "-A", "INPUT", "-p", f.Proto, "--dport", fmt.Sprintf("%d", f.Port), "-j", "ACCEPT"}
			if f.Comment != "" {
				argv = append(argv, "-m", "comment", "--comment", f.Comment)
			}
			return argv
		}
		return nil
	default:
		return nil
	}
}

func ifaceArgv(i IfaceOp) []string {
	verb := map[string]string{"up": "up", "down": "down"}[i.Action]
	return []string{"ip", "link", "set", "dev", i.Name, verb}
}

func runOp(dry bool, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty argv")
	}
	if dry {
		fmt.Printf("DRY-RUN: %v\n", argv)
		return nil
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
