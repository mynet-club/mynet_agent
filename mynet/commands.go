package mynet

import (
	"fmt"
	"os"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/utils"
)

// 子命令面（D-7 / AGENTS.md §1.2）：
//
//	setup · install · uninstall · update · config
//	detector · metrics · apply · service · driver
//	status · monitor · refresh-* · check · nodes · platform
//
// 每个子命令 = 一次 exec，用完即抛。mynetd 只负责分派到这里。
const usage = `mynet — MyNet 执行器（配置工具 + 管理员系统能力）

用法: mynet <命令> [参数]

系统能力（由 mynetd 分派）:
  detector          探测系统能力 → 写 $MYNET_HOME/var/system.json
  metrics           采集系统指标 + VPN/peer 状态（--json，心跳数据源）
  apply             应用配置：--active 写驱动投影 · --auto 装 OS 路由/防火墙 · --clear 清理
  service           进程生命周期（start|stop|reload|status <gnb|wireguard>）
  driver            驱动管理（列/装/升）

配置工具:
  setup            --local | --online --tenant T（配置树 v2）
  auth              账号态：login | status | logout（向导第 1 步）
  tenants           列出可用` + termZone + `（向导第 2 步）
  node              节点归属：claim（向导第 3 步：选节点 + 密钥 + 上传公钥）
  install           安装到系统
  uninstall         卸载
  update            自更新 / 组件升级
  config            export | import | use | pull | sync | parity
                    pull=下载+投影 · sync=pull+OS 路由 · refresh-*=只渲染本地 bundle
  plugin            list | install | enable | disable | status

只读状态:
  status            节点/链路状态
  subject           定位 gnb 主体（bin/conf/map/ctl，脚本排障入口）
  route doctor      连通性机测（路由/回程/计数/对等，判读见排错手册）
  firewall doctor   防火墙两层检查（云侧清单 + 机内 nft/iptables + fw4 持久化）
  gate              再部署门禁（G-subject/route/fw/sync/update 五闸）
  tree              配置树诊断/收敛（doctor | consolidate）
  monitor           持续监控输出
  check             自检
  nodes             节点列表
  platform          平台标识
  refresh-<域>      驱动配置刷新（gnb node/route/address/keys）

环境:
  MYNET_HOME        安装/数据根目录（默认可执行文件上推一级）
`

// Run 执行器入口。返回进程退出码。
func Run(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	cmd := args[0]
	rest := args[1:]

	// 不带 home 直接报错：**不再按 exe 目录猜**。OpenWrt 下可执行在 /usr/sbin，
	// 按 exe 推 home 会得到 /usr/sbin → 读到错的配置树（实测 `mynet config status`
	// 会静默返回「未初始化」而不是报错）。允许不需要配置树的命令；
	// install/uninstall 自带 --home/MYNET_HOME 解析（解析不到会自行报错）。
	switch cmd {
	case "help", "-h", "--help", "version", "-v", "--version", "platform",
		"install", "uninstall":
		// 无需 home
	default:
		if os.Getenv("MYNET_HOME") == "" {
			fmt.Fprintln(os.Stderr,
				"✗ 未设 MYNET_HOME：export MYNET_HOME=/etc/mynet 或用 --home DIR。"+
					"（不再按 exe 目录猜 home，以免读错配置树）")
			return 1
		}
	}

	if strings.HasPrefix(cmd, "refresh") {
		return cmdRefresh(args)
	}
	switch strings.SplitN(cmd, "-", 2)[0] {
	case "detector":
		return cmdDetector(rest)
	case "metrics":
		return cmdMetrics(rest)
	case "apply":
		return cmdApply(rest)
	case "service":
		return cmdService(rest)
	case "driver":
		return cmdDriver(rest)
	case "setup":
		return cmdSetupV2(rest)
	case "auth":
		return cmdAuth(rest)
	case "tenants":
		return cmdTenants(rest)
	case "node":
		return cmdNode(rest)
	case "status":
		return cmdStatus(rest)
	case "guest":
		// G5.b：guest 路由写面归执行器（唯一 writer）
		return cmdGuest(rest)
	case "nodeface":
		// G5：OpenWrt Lua 无 X25519，C-2 签名委托执行器
		return cmdNodeFace(rest)
	case "node-status":
		// G3：完整节点状态 JSON（localapi / mynetd 经 Ops 调用）
		return cmdNodeStatusJSON(rest)
	case "subject":
		return cmdSubject(rest)
	case "route":
		// route doctor —— 连通性机测子集（N4）
		// route exists —— G3：OS 路由存在性（mynetd 经 Ops，不自己 exec ip/route）
		if len(rest) > 0 && rest[0] == "doctor" {
			return cmdRouteDoctor(rest[1:])
		}
		if len(rest) > 0 && rest[0] == "exists" {
			return cmdRouteExists(rest[1:])
		}
		fmt.Fprintln(os.Stderr, "用法: mynet route doctor [--json] [--peer VIP]... | mynet route exists --dest CIDR | --host IP")
		return 2
	case "firewall":
		// firewall doctor —— 两层检查：云侧清单 + 机内 nft/iptables（N5）
		// firewall sync —— OpenWrt：include 持久化 + UCI device 跟随 tun 名
		if len(rest) > 0 && rest[0] == "doctor" {
			return cmdFirewallDoctor(rest[1:])
		}
		if len(rest) > 0 && rest[0] == "sync" {
			return cmdFirewallSync(rest[1:])
		}
		fmt.Fprintln(os.Stderr, "用法: mynet firewall doctor [--json] [--port N] | sync")
		return 2
	case "gate":
		// P7 再部署门禁：G-subject/route/fw/sync/update
		return cmdGate(rest)
	case "tree":
		return cmdTree(rest)
	case "check":
		return cmdCheck(rest)
	case "nodes":
		return cmdNodes(rest)
	case "platform":
		return cmdPlatform(rest)
	case "refresh":
		return cmdRefresh(append([]string{cmd}, rest...))
	case "install":
		return cmdInstall(rest)
	case "uninstall":
		return cmdUninstall(rest)
	case "update":
		return cmdUpdate(rest)
	case "config":
		return cmdConfig(rest)
	case "dns":
		// DNS 接管诊断/紧急恢复（保险丝③）
		return cmdDNS(rest)
	case "plugin":
		return cmdPlugin(rest)
	case "proxy":
		return cmdProxy(rest)
	case "monitor":
		return cmdStub(cmd, rest)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	case "version", "-v", "--version":
		fmt.Println(Version)
		// B4：版本戳三句话 —— 基线 / 补丁 / 构建者（ldflags 注入，dev 为默认）
		fmt.Printf("baseline: %s\n", utils.GnbBaseline)
		fmt.Printf("patches:  %s\n", utils.GnbPatchStack)
		fmt.Printf("builder:  %s\n", utils.BuildStamp)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n%s", cmd, usage)
		return 2
	}
}

func cmdStub(cmd string, _ []string) int {
	fmt.Fprintf(os.Stderr, "mynet %s: 尚未实现（按 dev-plan 顺序表推进）\n", cmd)
	return 1
}
