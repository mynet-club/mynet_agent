package mynet

import (
	"fmt"
	"os"

	"github.com/mynet-club/mynet_agent-V2/shared"
)

// cmdApply — F-2/F-3/F-5：唯一写入者。
//
// 读 system.json 决定 exec 什么工具，对 gnb0 与 wg0 一视同仁（不感知驱动）。
// 用法: mynet apply [--dry-run] <plan.json>
func cmdApply(args []string) int {
	dry := false
	planPath := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dry = true
		case "--out", "--plan":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "缺少参数值")
				return 2
			}
			i++
			planPath = args[i]
		default:
			planPath = args[i]
		}
	}
	if planPath == "" {
		fmt.Fprintln(os.Stderr, "用法: mynet apply [--dry-run] <plan.json>")
		return 2
	}

	snap, err := shared.ReadSnapshot(shared.DefaultSnapshotPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "读 system.json 失败（先跑 mynet detector）: %v\n", err)
		return 1
	}

	plan, err := readApplyPlan(planPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读 plan 失败: %v\n", err)
		return 1
	}

	return executeApply(snap, plan, dry)
}

func cmdService(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: mynet service <start|stop|reload|status> <name>")
		return 2
	}
	fmt.Fprintf(os.Stderr, "mynet service %v: 委托 procd/systemd/launchd（E 阶段落地）\n", args)
	return 1
}

func cmdDriver(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: mynet driver <list|install|upgrade> [name]")
		return 2
	}
	fmt.Fprintf(os.Stderr, "mynet driver %v: 调 upgrade_service（D-9）\n", args)
	return 1
}
