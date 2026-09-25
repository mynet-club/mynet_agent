package mynet

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/mynet-club/mynet_agent-V2/mynet/driver"
	"github.com/mynet-club/mynet_agent-V2/mynet/upgrade"
	"github.com/mynet-club/mynet_agent-V2/shared"
	"github.com/mynet-club/mynet_agent-V2/shared/domain"
)

// cmdApply — F-2/F-3/F-5：唯一写入者。
//
// 读 system.json 决定 exec 什么工具，对 gnb0 与 wg0 一视同仁（不感知驱动）。
//
// 用法:
//
//	mynet apply [--dry-run] <plan.json>   # 执行一份已生成的计划
//	mynet apply [--dry-run] --auto        # 直接从 $MYNET_HOME/var/bundle.json 生成并执行
//
// --auto 是给守护进程用的：mynetd 收到 bundle_changed 后要「刷配置 → 起驱动 →
// 装路由」一条龙，中间不该出现一个临时 plan 文件（多一个落盘点就多一处
// 「打印的计划」与「真执行的」可能不一致的地方）。两条路径共用 planFromBundle()。
func cmdApply(args []string) int {
	dry := false
	auto := false
	planPath := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dry = true
		case "--auto":
			auto = true
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

	snap, err := shared.ReadSnapshot(shared.DefaultSnapshotPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "读 system.json 失败（先跑 mynet detector）: %v\n", err)
		return 1
	}

	if auto {
		env, err := loadBundleEnvelope()
		if err != nil {
			fmt.Fprintf(os.Stderr, "apply --auto: 读 bundle 失败（先让 mynetd 拉一版）: %v\n", err)
			return 1
		}
		plan := planFromBundle(env)
		if len(plan.Routes) == 0 && len(plan.Firewall) == 0 && len(plan.Interfaces) == 0 {
			// 不是错误：没有对端时本来就没有路由要装。但要**说出来**，
			// 否则「apply 成功」和「apply 什么都没做」在日志里长得一样。
			fmt.Println("apply --auto: bundle 里没有要应用的路由/防火墙/接口（无对端？）")
			return 0
		}

		return executeApply(snap, &plan, dry)
	}

	if planPath == "" {
		fmt.Fprintln(os.Stderr, "用法: mynet apply [--dry-run] <plan.json> | --auto")
		return 2
	}

	plan, err := readApplyPlan(planPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读 plan 失败: %v\n", err)
		return 1
	}

	return executeApply(snap, plan, dry)
}

// cmdService — 驱动进程生命周期。
//
//	mynet service <start|stop|reload|status> <gnb|wireguard>
//
// ⚠️ 此前这里是桩：直接返回 1 并打印「委托 procd/systemd/launchd（E 阶段落地）」。
// 后果是 GNBDriver.Start()/Stop() 全仓**没有任何调用者**（Reload() 也零调用者），
// 配置渲染得再对，隧道也永远起不来 —— 0→1 链在最后一公里断掉。
//
// 现在直接驱动 driver 的 Start/Stop，不依赖外部监管也能后台跑：gnb 的 node.conf
// 写 `daemon on`，gnb 自己双 fork、父进程立即 exit(0)（gnb.c:397-399 →
// gnb_daemon.c:36-48），子进程写 <conf>/gnb.pid（gnb.c:401）。
// 要 systemd/procd 监管时走 `mynet install` 装单元、用 `gnb --systemd` 拉起 ——
// 那时 daemon 键被忽略、保持前台（gnb_argv.c:439-440 + gnb_conf_file.c:458），
// 两种模式共用同一份 node.conf。
//
// ⚠️ 已知缺口（wireguard）：refresh-config 把配置写到 var/wg.conf，而
// WireGuardDriver.Start() 走 `wg-quick up <iface>`，wg-quick 只读
// /etc/wireguard/<iface>.conf（wireguard.go:146,158,164）—— 路径对不上。
// gnb 那条链已通；wg 这条要等 install 阶段落地，此处不假装它能用。
func cmdService(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet service <start|stop|reload|status> [gnb|wireguard]")
		return 2
	}
	action := args[0]
	name := ""
	if len(args) >= 2 {
		name = args[1]
	} else {
		// 驱动名可省略：从 bundle 的 driver 字段取。守护进程走这条路 ——
		// 它不该硬编码驱动类型，否则 gnb 节点与 wg 节点要跑两套逻辑。
		env, err := loadBundleEnvelope()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mynet service: 未指定驱动且读 bundle 失败: %v\n", err)
			return 1
		}
		name = string(env.Driver)
		if name == "" {
			fmt.Fprintln(os.Stderr, "mynet service: bundle 里没有 driver 字段，请显式指定 gnb|wireguard")
			return 1
		}
		fmt.Printf("未指定驱动，按 bundle 取 %s\n", name)
	}

	var typ domain.VPNDriverType
	switch name {
	case "gnb":
		typ = domain.DriverTypeGNB
	case "wireguard", "wg":
		typ = domain.DriverTypeWireGuard
	default:
		fmt.Fprintf(os.Stderr, "未知驱动: %s（只支持 gnb|wireguard）\n", name)
		return 2
	}

	d, err := buildLifecycleDriver(typ)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mynet service: %v\n", err)
		return 1
	}

	switch action {
	case "start":
		if err := d.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "start %s: %v\n", name, err)
			return 1
		}
		fmt.Printf("started %s\n", name)
		return 0
	case "stop":
		if err := d.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "stop %s: %v\n", name, err)
			return 1
		}
		fmt.Printf("stopped %s\n", name)
		return 0
	case "reload":
		if err := d.Reload(); err != nil {
			fmt.Fprintf(os.Stderr, "reload %s: %v\n", name, err)
			return 1
		}
		fmt.Printf("reloaded %s\n", name)
		return 0
	case "status":
		st, err := d.Status()
		if err != nil {
			fmt.Fprintf(os.Stderr, "status %s: %v\n", name, err)
			return 1
		}
		fmt.Printf("%s running=%v node_id=%s\n", name, st.Running, st.NodeID)
		if !st.Running {
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知动作: %s（只支持 start|stop|reload|status）\n", action)
		return 2
	}
}

// buildLifecycleDriver 按 refresh-config 的**同一套路径约定**构造驱动。
//
// 两处必须一致：写配置的目录和起进程的目录若对不上，症状是「配置明明写了，
// 起的却是另一个目录」—— 而这正是此前 GNBDriver 默认 ConfDir
// （driver/gnb/conf/<nodeId>）与 refresh-config 覆盖值（var/gnb）不一致的形态。
func buildLifecycleDriver(typ domain.VPNDriverType) (domain.VPNLifecycleDriver, error) {
	env, err := loadBundleEnvelope()
	if err != nil {
		return nil, fmt.Errorf("读 bundle 失败（先让 mynetd 拉一版，或跑 mynet refresh-config）: %w", err)
	}

	switch typ {
	case domain.DriverTypeGNB:
		if env.Gnb == nil {
			return nil, fmt.Errorf("bundle 里没有 gnb 段")
		}
		nodeID, err := strconv.ParseInt(env.Gnb.Identity.NodeID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bundle 的 gnb.identity.node_id 非法: %q", env.Gnb.Identity.NodeID)
		}
		d := driver.NewGNBDriver(nodeID)
		d.ConfDir = gnbConfDir()

		return d, nil
	case domain.DriverTypeWireGuard:
		d := driver.NewWireGuardDriver(env.Node.Interface, 0)
		d.ConfPath = filepath.Join(varDir(), "wg.conf")

		return d, nil
	default:
		return nil, fmt.Errorf("未知驱动类型 %q", typ)
	}
}

func cmdDriver(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: mynet driver <list|install|upgrade> [gnb|wireguard]")
		return 2
	}
	svc := upgrade.NewService("https://ctl.mynet.club")
	switch args[0] {
	case "list":
		list, err := svc.List()
		if err != nil {
			fmt.Fprintf(os.Stderr, "list: %v\n", err)
			return 1
		}
		for _, a := range list {
			fmt.Printf("%s\t%s\t%s\n", a.Component, a.Version, a.Platform)
		}
		return 0
	case "install", "upgrade":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "需要 gnb|wireguard")
			return 2
		}
		if err := svc.InstallDriver(upgrade.Component(args[1])); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", args[0], err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知 driver 子命令: %s\n", args[0])
		return 2
	}
}
