package mynet

import (
	"fmt"
	"os"
	"strings"
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
  apply             应用配置：驱动原生配置 + OS 路由/防火墙
  service           进程生命周期（start|stop|reload|status <gnb|wireguard>）
  driver            驱动管理（列/装/升）

配置工具:
  setup             首次配置（生成节点密钥 + 写 mynet.conf）
  auth              账号态：login | status | logout（向导第 1 步）
  tenants           列出可用租户（向导第 2 步）
  node              节点归属：claim（向导第 3 步：选节点 + 密钥 + 上传公钥）
  install           安装到系统
  uninstall         卸载
  update            自更新 / 组件升级
  config            show | check

只读状态:
  status            节点/链路状态
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
		return cmdSetup(rest)
	case "auth":
		return cmdAuth(rest)
	case "tenants":
		return cmdTenants(rest)
	case "node":
		return cmdNode(rest)
	case "status":
		return cmdStatus(rest)
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
	case "config", "monitor":
		return cmdStub(cmd, rest)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	case "version", "-v", "--version":
		fmt.Println(Version)
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
