package mynet

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/mynet-club/mynet_agent/mynet/driver"
	"github.com/mynet-club/mynet_agent/mynet/upgrade"
	"github.com/mynet-club/mynet_agent/shared"
	"github.com/mynet-club/mynet_agent/shared/config"
	"github.com/mynet-club/mynet_agent/shared/domain"
	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/tunnelstate"
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
		// system.json 是 apply 的素材（L6 判据）。编排层常漏跑 detector
		// （OpenWrt init post_start / Lua apply_routes 直调本命令），缺素材
		// 硬失败会把「隧道通、路由无」留在现场 —— 这里自愈一次再读。
		fmt.Fprintln(os.Stderr, "system.json 缺失，先跑 detector")
		if rc := cmdDetector(nil); rc != 0 {
			fmt.Fprintf(os.Stderr, "读 system.json 失败（先跑 mynet detector）: %v\n", err)
			return 1
		}
		snap, err = shared.ReadSnapshot(shared.DefaultSnapshotPath())
		if err != nil {
			fmt.Fprintf(os.Stderr, "读 system.json 失败（detector 后仍缺）: %v\n", err)
			return 1
		}
	}

	// --clear：只清**自己装的**隧道路由（dev = 隧道口上的），与 --auto 对称。
	// 绝不按目的地盲删 —— 见 apply_plan.go 的所有权闸。
	if clear {
		return clearTunnelRoutes(snap, dry)
	}

	if auto {
		var plan ApplyPlan
		// local 模式没有 bundle —— L6 计划从 intent.routes 来（与 bundle 同语义）。
		st := &config.Store{}
		_, mode, merr := st.ActiveSource()
		if merr == nil && mode != nil && mode.Mode == domain.ModeLocal {
			li, lerr := st.LoadLocalIntent()
			if lerr != nil {
				fmt.Fprintf(os.Stderr, "apply --auto: 读 local intent 失败: %v\n", lerr)
				return 1
			}
			plan, err = planFromLocalIntent(li)
		} else {
			env, berr := loadBundleEnvelope()
			if berr != nil {
				fmt.Fprintf(os.Stderr, "apply --auto: 读 bundle 失败（先让 mynetd 拉一版）: %v\n", berr)
				return 1
			}
			plan, err = planFromBundle(env)
		}
		if err != nil {
			// 计划不成立 = 一条都不装。半装（跳过对不上的路由）会把 FIB
			// 留在「部分新部分旧」的状态，比不装更难排查。
			fmt.Fprintf(os.Stderr, "apply --auto: %v\n", err)
			return 1
		}
		if len(plan.Routes) == 0 && len(plan.Firewall) == 0 && len(plan.Interfaces) == 0 && plan.ProxyExitDevice == "" {
			// 不是错误：没有对端时本来就没有路由要装。但要**说出来**，
			// 否则「apply 成功」和「apply 什么都没做」在日志里长得一样。
			fmt.Println("apply --auto: 没有要应用的路由/防火墙/接口（无对端？）")
			return 0
		}

		rc := executeApply(snap, &plan, dry)
		if rc == 0 && !dry {
			// 防火墙持久化同步（上层合同见 firewall_persist_common.go）：
			// OpenWrt 登记 UCI include（fw4 reload 后重放），Linux 按后端登记。
			syncFirewallAfterTunnelUp()
		}
		return rc
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
// wg 侧（D4-E 已收口）：conf 落节点唯一存放位 conf/<form>/<nodeId>/wg.conf，
// Start 走 `ip link add` + `wg setconf <iface> <file>`（setconf 吃任意路径），
// 与 OpenWrt init 兜底同一路线；热同步 `wg syncconf` 同文件。
func cmdService(args []string) int {
	// C3：service 驱动进程生命周期，必须显式 MYNET_HOME
	home, code := requireHome()
	if code != 0 {
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
		// 启停意图（开机期望隧道）：用户说 start 就记 true —— 与成败无关，
		// 意图只由启停动作产生（shared/tunnelstate，mynetd 开机恢复读它）。
		_ = tunnelstate.MarkWant(true, name)
		// 身份门禁：无私钥不起隧道（口径 B）
		if err := RequireLocalIdentity(); err != nil {
			fmt.Fprintf(os.Stderr, "start %s: %v\n", name, err)
			return 1
		}
		if err := migratePCAcceleration(); err != nil {
			fmt.Fprintln(os.Stderr, "加速偏好迁移失败:", err)
			return 1
		}
		// 节点配置先行：确保 conf/<form>/<nodeId>/ 下驱动配置与当前意图/bundle 同步。
		if err := ApplyFromActive(false); err != nil {
			fmt.Fprintf(os.Stderr, "start %s: 同步节点配置失败: %v\n", name, err)
			return 1
		}
		if err := d.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "start %s: %v\n", name, err)
			return 1
		}
		fmt.Printf("started %s\n", name)
		// 启动验收（缺口⑥）：waitAlive 之后还要看 tun —— gnb 活了但 tun 未起
		// （tun 名被占/权限不足）时进程在、零流量，只报 "started" 就是骗人。
		// tun 不在，后面 apply 装路由也是白装，先 fail loud。
		if err := verifyTunAfterStart(name, func() TunHealth {
			return currentTunHealth(home)
		}, 3, 500*time.Millisecond); err != nil {
			fmt.Fprintf(os.Stderr, "✗ start %s: %v\n", name, err)
			return 1
		}
		// 隧道接口**已建立**，此刻落地防火墙持久化 —— 产物里的 DEV 要对上真实
		// 接口名，早于这一步算出来的名字可能与实际投影不一致。放在这里而不是
		// mynetd 启动时：mynetd 是纯通信层，不碰系统能力（AGENTS.md §1.2）。
		// applyAfterStart 先安装 WAN 授权边界，再同步重放钩子。
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
		// 侧车编排（proxy ∥ dns-plan → dns-hook）：只同步**已启用**的，
		// 失败不回滚隧道。托盘/WebUI 的启停都汇到这里，故挂在此处。
		SyncSidecarsOnTunnelUp()
		return 0
	case "stop":
		_ = tunnelstate.MarkWant(false, name)
		// 栈式逆序收侧车：dns-hook → dns-restore（不再单独跑 RestoreSystemDNS，
		// 已并入 restoreDNSSidecar，避免双跑）。
		SyncSidecarsOnTunnelDown()
		if err := d.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "stop %s: %v\n", name, err)
			return 1
		}
		// 路由回收归编排（E 层唯一写入者 = mynet apply）：只清自己装的隧道路由，
		// 所有权闸不盲删 —— 以前靠 init 的 apply --clear 兜，P1 上收后由 stop 自带。
		if code := cmdApply([]string{"--clear"}); code != 0 {
			fmt.Fprintf(os.Stderr, "stop %s: 隧道已停，但路由回收未全部成功（exit %d）\n", name, code)
			return code
		}
		fmt.Printf("stopped %s\n", name)
		return 0
	case "reload", "restart":
		_ = tunnelstate.MarkWant(true, name)
		// UI 用语是「重启」；CLI 历史名是 reload。两者同一动作。
		// 先投影再重启：refresh-config 只写原生配置，运行投影在 ApplyFromActive
		// 里生成 —— 旧序「先 Reload 后 applyAfterStart」让 gnb 重启读的是旧投影，
		// 配置永远差一代。
		if err := ApplyFromActive(false); err != nil {
			fmt.Fprintf(os.Stderr, "reload %s: 投影失败（gnb 保持旧配置）: %v\n", name, err)
			return 1
		}
		// 重载前先回收旧隧道口路由：macOS 下 gnb 重启会换 utun 编号，先清旧口可避免残留路由指向已失效 utun。
		_ = cmdApply([]string{"--clear"})
		if err := d.Reload(); err != nil {
			fmt.Fprintf(os.Stderr, "reload %s: %v\n", name, err)
			return 1
		}
		fmt.Printf("reloaded %s\n", name)
		if err := verifyTunAfterStart(name, func() TunHealth {
			return currentTunHealth(home)
		}, 3, 500*time.Millisecond); err != nil {
			fmt.Fprintf(os.Stderr, "✗ reload %s: %v\n", name, err)
			return 1
		}
		if code := applyAfterStart(); code != 0 {
			fmt.Fprintf(os.Stderr, "reload %s: apply（投影+路由）未全部成功（exit %d）\n", name, code)
			return code
		}
		SyncSidecarsOnTunnelUp()
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

// buildLifecycleDriver 按统一两层目录模型构造驱动：
// 写配置与起进程均直接指向唯一节点配置目录 $MYNET_HOME/conf/<form>/<nodeId>/，
// 不再经过 /var/run/mynet/<nodeId> 配置投影。
func buildLifecycleDriver(typ domain.VPNDriverType) (domain.VPNLifecycleDriver, error) {
	// local 模式没有 bundle —— 身份在 conf/local/intent.json。CLI service 主链
	// （start/stop/reload/status）在本地模式也必须能跑，不能只认 bundle。
	store := &config.Store{}
	if _, mode, err := store.ActiveSource(); err == nil && mode != nil && mode.Mode == domain.ModeLocal {
		li, lerr := store.LoadLocalIntent()
		if lerr != nil {
			return nil, fmt.Errorf("load local intent: %w", lerr)
		}
		return buildLocalLifecycleDriver(typ, li)
	}
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
		if p := wgNodeConfPath(env.Node.ID); p != "" {
			d.ConfPath = p
		}

		return d, nil
	default:
		return nil, fmt.Errorf("未知驱动类型 %q", typ)
	}
}

// buildLocalLifecycleDriver local 模式驱动：身份/目录与 applyLocal 同源。
// wireguard 的 local 意图没有接口名（applyLocal 也只有 gnb 线），如实报错。
func buildLocalLifecycleDriver(typ domain.VPNDriverType, li *domain.LocalIntent) (domain.VPNLifecycleDriver, error) {
	switch typ {
	case domain.DriverTypeGNB:
		nodeID, err := strconv.ParseInt(li.Node.ID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("intent.node.id 非法: %q", li.Node.ID)
		}
		d := driver.NewGNBDriver(nodeID)
		d.ConfDir = gnbConfDir()
		return d, nil
	case domain.DriverTypeWireGuard:
		return nil, fmt.Errorf("local 意图不含 wireguard 接口名，service 命令暂只支持 online 模式的 wireguard")
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
