package mynet

import (
	"fmt"
	"os"
	"strconv"

	"github.com/mynet-club/mynet_agent/mynet/driver"
	"github.com/mynet-club/mynet_agent/mynet/upgrade"
	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/logger"
)

// cmdApply — F-2/F-3/F-5：唯一写入者。
//
// 路由 SSOT = bundle.network.routes（不是 gnb route.conf，见 ApplyPlan 注释）。
// 边界 SSOT = knowledge-base/spec/route-firewall-split.md。
//
// 三面语义（不可混）：
//
//	--active  投影：mode.json/intent/bundle → driver/**（不装 OS 路由）
//	--auto    系统：OS 路由/防火墙/NAT（不改 driver conf）
//	--clear   清理：只删自己装的隧道路由（所有权闸，不盲删目的地）
//
// 读 system.json 决定 exec 什么工具，对 gnb0 与 wg0 一视同仁（不感知驱动）。
//
// 用法:
//
//	mynet apply [--dry-run] --active       # 只写驱动投影
//	mynet apply [--dry-run] --auto         # 只装 OS 路由/防火墙（读 var/bundle.json）
//	mynet apply [--dry-run] --active --auto  # 两面都做（推荐；service start 默认）
//	mynet apply [--dry-run] --clear        # 清自有隧道路由
//	mynet apply [--dry-run] <plan.json>    # 执行一份已生成的计划
//
// --auto 是给守护进程用的：mynetd 收到 bundle_changed 后要「刷配置 → 起驱动 →
// 装路由」一条龙，中间不该出现一个临时 plan 文件（多一个落盘点就多一处
// 「打印的计划」与「真执行的」可能不一致的地方）。两条路径共用 planFromBundle()。
func cmdApply(args []string) int {
	// C3：apply 写 driver 投影 / OS 路由，必须显式 MYNET_HOME
	if _, code := requireHome(); code != 0 {
		return code
	}
	dry := false
	auto := false
	active := false
	clear := false
	planPath := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dry = true
		case "--auto":
			auto = true
		case "--active":
			active = true
		case "--clear":
			clear = true
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

	// --active：从 mode.json / intent / bundle 生成 driver 投影
	// 可与 --auto 同用：先投影再装 OS（service start 的默认序）
	if active {
		if err := ApplyFromActive(dry); err != nil {
			fmt.Fprintf(os.Stderr, "apply --active: %v\n", err)
			return 1
		}
		if !auto && !clear {
			return 0
		}
	}

	snap, err := shared.ReadSnapshot(shared.DefaultSnapshotPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "读 system.json 失败（先跑 mynet detector）: %v\n", err)
		return 1
	}

	// --clear：只清**自己装的**隧道路由（dev = 隧道口上的），与 --auto 对称。
	// 绝不按目的地盲删 —— 见 apply_plan.go 的所有权闸。
	if clear {
		return clearTunnelRoutes(snap, dry)
	}

	if auto {
		env, err := loadBundleEnvelope()
		if err != nil {
			fmt.Fprintf(os.Stderr, "apply --auto: 读 bundle 失败（先让 mynetd 拉一版）: %v\n", err)
			return 1
		}
		plan, err := planFromBundle(env)
		if err != nil {
			// 计划不成立 = 一条都不装。半装（跳过对不上的路由）会把 FIB
			// 留在「部分新部分旧」的状态，比不装更难排查。
			fmt.Fprintf(os.Stderr, "apply --auto: %v\n", err)
			return 1
		}
		if len(plan.Routes) == 0 && len(plan.Firewall) == 0 && len(plan.Interfaces) == 0 {
			// 不是错误：没有对端时本来就没有路由要装。但要**说出来**，
			// 否则「apply 成功」和「apply 什么都没做」在日志里长得一样。
			fmt.Println("apply --auto: bundle 里没有要应用的路由/防火墙/接口（无对端？）")
			return 0
		}

		return executeApply(snap, &plan, dry)
	}

	if planPath == "" {
		fmt.Fprintln(os.Stderr, "用法: mynet apply [--dry-run] --active | --auto | --active --auto | --clear | <plan.json>")
		fmt.Fprintln(os.Stderr, "  --active  写驱动投影 driver/**（不装 OS 路由）")
		fmt.Fprintln(os.Stderr, "  --auto    装 OS 路由/防火墙（不改 driver conf）")
		fmt.Fprintln(os.Stderr, "  --clear   清自有隧道路由")
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
	// C3：service 驱动进程生命周期，必须显式 MYNET_HOME
	if _, code := requireHome(); code != 0 {
		return code
	}
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet service <start|stop|reload|restart|status> [gnb|wireguard]")
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
		// D4/§14：DNS 是**用户三选一**（不劫持 / 指定上游 / 交我们），默认不劫持。
		// 只有显式 mynet dns apply 或配置 dns.hijack=ours 才接管 ——
		// service start 自动劫持曾把网络干死（多网卡/目标不稳），禁止默认劫持。
		// stop 时仍还原（幂等），收掉遗留接管。
		// 起隧道后必须装路由，否则「起来了但 ping 不通」——
		// 流量没进隧道（实测 DEGRADED：22 条路由未安装）。
		if code := applyAfterStart(); code != 0 {
			fmt.Fprintf(os.Stderr, "start %s: 隧道已启动，但 apply（投影+路由）未全部成功（exit %d）\n", name, code)
			return code
		}
		return 0
	case "stop":
		// 还原遗留 DNS 接管（幂等；只有显式接管过才有备份）
		if isPCDNSPlatform() {
			if err := RestoreSystemDNS(); err != nil {
				fmt.Fprintf(os.Stderr, "⚠ DNS 还原失败: %v\n", err)
			} else if DNSRestoreNeeded() {
				fmt.Println("dns: 仍有未还原的接管（mynet dns restore）")
			} else {
				fmt.Println("dns: 无接管或已还原")
			}
		}
		if err := d.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "stop %s: %v\n", name, err)
			return 1
		}
		fmt.Printf("stopped %s\n", name)
		return 0
	case "reload", "restart":
		// UI 用语是「重启」；CLI 历史名是 reload。两者同一动作。
		if err := d.Reload(); err != nil {
			fmt.Fprintf(os.Stderr, "reload %s: %v\n", name, err)
			return 1
		}
		fmt.Printf("reloaded %s\n", name)
		if code := applyAfterStart(); code != 0 {
			fmt.Fprintf(os.Stderr, "reload %s: apply（投影+路由）未全部成功（exit %d）\n", name, code)
			return code
		}
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
		fmt.Fprintf(os.Stderr, "未知动作: %s（只支持 start|stop|reload|restart|status）\n", action)
		return 2
	}
}

// applyAfterStart 启动/重启后：先投影再装 OS 路由（route-firewall-split §3.1）。
// 复用 `apply --active --auto` 同一套策略（防劫持跳过、所有权闸等）。
func applyAfterStart() int {
	logger.Comp("apply").Info("启动后 apply：投影 + OS 路由")
	// 缺 system.json 时 apply --auto 会直接失败、路由装不全 —— 先补 detector。
	if _, err := shared.ReadSnapshot(shared.DefaultSnapshotPath()); err != nil {
		logger.Comp("apply").Info("system.json 缺失，先跑 detector")
		if rc := cmdDetector(nil); rc != 0 {
			logger.Comp("apply").Warn("detector 失败，apply 可能不完整")
		}
	}
	return cmdApply([]string{"--active", "--auto"})
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
		// 接口名只经 TunDevice() 这一个解析口，且**不给「空则 wg0」的兜底**：
		// wg-quick 只要接口名存在就执行成功，猜错名字等于把这台机器的流量搬进
		// 别人的隧道 —— 宁可这里报错、服务起不来，也不下一份会把流量搬走的配置。
		dev, err := env.TunDevice()
		if err != nil {
			return nil, fmt.Errorf("wireguard 接口名: %w", err)
		}
		d := driver.NewWireGuardDriver(dev, 0)
		// 路径对齐 wg-quick 原生位（见 wireguard.go WriteIntent 默认值）。
		// 此前写 var/wg.conf 而 wg-quick 只读 /etc/wireguard/，Start 必失败。
		d.ConfPath = "/etc/wireguard/" + d.Interface + ".conf"

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
