// Package commands — D-6 内化原 `bin/mynet` 的五条命令链。
//
// 旧 conf/command.linux.json 把 refresh-routes / refresh-address 等写成
// 外部 exec；现改为 mynetd 分派 → 执行器 mynet 的结构化子命令，
// mynet 降级为可选运维 CLI（人手敲），daemon 不再依赖 bin/mynet 路径。
package commands

// Request 是一条待执行的指令（来自 WS / 本机 API）。
type Request struct {
	ID   string            `json:"id"`
	Op   string            `json:"op"`             // 见 Op* 常量
	Args map[string]string `json:"args,omitempty"` // node_id / vpn_type / generation …
}

// Result 执行结果，经 WS 上报 back。
type Result struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Stdout  string `json:"stdout,omitempty"`
	Stderr  string `json:"stderr,omitempty"`
	Error   string `json:"error,omitempty"`
	Elapsed int64  `json:"elapsed_ms"`
}

// 五条命令链（原 command.linux.json 中 command=bin/mynet 的项）
const (
	OpRefreshRoutesGnb  = "gnb.refresh.route"   // mynet refresh-routes --auto
	OpRefreshAddressGnb = "gnb.refresh.address" // mynet refresh-address --auto
	OpRefreshRoutesWG   = "wg.refresh.route"    // + --vpn-type wireguard
	OpRefreshAddressWG  = "wg.refresh.address"
	OpRouteRefresh      = "route.refresh" // 同 refresh-routes
	OpRouteUpdate       = "route.update"  // mynet apply（L6 写入）
	OpNodeRefresh       = "node.refresh"
	OpNodeUpdate        = "node.update"
	OpDetector          = "detector"
	OpMetrics           = "metrics"
	OpServiceStart      = "service.start"
	OpServiceStop       = "service.stop"
	OpServiceReload     = "service.reload"
)

// plugin.proxy.* 已下架（mynet_proxy deprecated），不进 Op 面。

// Executor 执行器接口：桌面 exec mynet；OpenWrt 装配时换 lua。
type Executor func(argv []string) (stdout, stderr string, err error)

// Plan 把结构化 Op 展开成执行器 argv。
// 不在此处做系统能力——只做「翻译」。
func Plan(req Request, nodeID string) ([]string, bool) {
	nid := req.Args["node_id"]
	if nid == "" {
		nid = nodeID
	}
	vpn := req.Args["vpn_type"]

	switch req.Op {
	case OpRefreshRoutesGnb, OpRouteRefresh:
		argv := []string{"refresh-routes", "--auto"}
		if nid != "" {
			argv = append(argv, "--node-id", nid)
		}
		return argv, true
	case OpRefreshRoutesWG:
		argv := []string{"refresh-routes", "--auto", "--vpn-type", "wireguard"}
		if nid != "" {
			argv = append(argv, "--node-id", nid)
		}
		return argv, true
	case OpRefreshAddressGnb:
		return []string{"refresh-address", "--auto"}, true
	case OpRefreshAddressWG:
		return []string{"refresh-address", "--auto", "--vpn-type", "wireguard"}, true
	case OpRouteUpdate:
		argv := []string{"apply"}
		if p := req.Args["plan"]; p != "" {
			argv = append(argv, p)
		}
		return argv, true
	case OpNodeRefresh, OpNodeUpdate:
		// ⚠️ 是 refresh-**config**，不是 refresh-nodes。执行器的 refresh 域只有
		// routes|address|config|keys（mynet/cmd_refresh.go:43-55），此前这里写的
		// "refresh-nodes" 从来不存在 → 一律「未知 refresh 域: nodes」退出码 2。
		// 而 bundle_changed 恰恰映射到本 Op，于是「配置包变更 → 刷新节点配置」
		// 这条链**始终以失败收尾**，且失败信息看起来像执行器坏了而不是映射错了。
		// 语义依据：spec/unified-platform-architecture.md:741 —— node.refresh 的
		// 职责就是写 node.conf（驱动原生节点配置），即 refresh-config 做的事。
		// 守护见 backmap_test.go 的 TestPlanRefreshDomainsExistInExecutor。
		argv := []string{"refresh-config", "--auto"}
		if nid != "" {
			argv = append(argv, "--node-id", nid)
		}
		_ = vpn
		return argv, true
	case OpDetector:
		return []string{"detector", "--json"}, true
	case OpMetrics:
		return []string{"metrics", "--json"}, true
	case OpServiceStart:
		return []string{"service", "start", req.Args["name"]}, true
	case OpServiceStop:
		return []string{"service", "stop", req.Args["name"]}, true
	case OpServiceReload:
		return []string{"service", "reload", req.Args["name"]}, true
	default:
		return nil, false
	}
}

// Run 翻译并执行一条指令。
func Run(ex Executor, req Request, nodeID string) Result {
	argv, ok := Plan(req, nodeID)
	if !ok {
		return Result{ID: req.ID, OK: false, Error: "unknown op: " + req.Op}
	}
	stdout, stderr, err := ex(argv)
	res := Result{ID: req.ID, Stdout: stdout, Stderr: stderr, OK: err == nil}
	if err != nil {
		res.Error = err.Error()
	}
	return res
}
