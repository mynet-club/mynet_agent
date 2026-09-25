// mynetd — 纯通信层常驻进程。
//
// root 要求**分模式**（用户 2026-09-25 裁定：节点模式要管 tun，root 是必须的）：
//   - console 模式（-console）：D-8/D-12，无 WS、无节点身份、不碰驱动 → **不需要 root**
//   - 节点模式（配了 MYNET_GATEWAYS）：要起 gnb（建 tun）、写内核路由、读 gnb.map
//     → **需要 root**。生产走 deploy/systemd/mynetd.service。
//     非 root 时不 fail-fast（理由见下面那段警告的注释），但会高声警告。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mynet-club/mynet_agent-V2/mynetd"
	"github.com/mynet-club/mynet_agent-V2/mynetd/commands"
	"github.com/mynet-club/mynet_agent-V2/mynetd/localapi"
	"github.com/mynet-club/mynet_agent-V2/mynetd/transport"
	"github.com/mynet-club/mynet_agent-V2/shared"
	"github.com/mynet-club/mynet_agent-V2/shared/logger"
	"github.com/mynet-club/mynet_agent-V2/shared/utils"
)

func main() {
	var (
		listen  = flag.String("listen", mynetd.DefaultLocalAPI, "本机 API 监听")
		console = flag.Bool("console", false, "console 模式：无 WS、无节点身份（D-12）")
		once    = flag.Bool("once", false, "只探测一次心跳就退出（调试）")
		version = flag.Bool("version", false, "打印版本并退出")
	)
	flag.Parse()

	// 日志装配必须在任何日志输出之前 —— 否则前几条会走默认 handler，
	// 于是同一次运行里混着两种格式，日志系统解析会断在那几行上。
	// 默认 info + text + stderr；MYNET_LOG_FORMAT=json 切成结构化（V4-4）。
	if err := logger.InitFromEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "logger: %v\n", err)
		os.Exit(2)
	}
	defer logger.Close()

	// 守护进程最需要「装的是哪个版本」——升级排查、心跳上报版本、远程排障都靠它。
	if *version {
		fmt.Printf("mynetd %s\n", utils.AppVersion)
		return
	}

	// root 要求是**分模式**的，此前这条提示是模式盲的（对谁都说「无需 root」），
	// 于是节点模式下三件必须特权的事全部静默失败 —— Phase 6 实测撞过：
	//
	//   · console 模式（-console，D-8/D-12）：无 WS、无节点身份、不碰驱动，
	//     确实**不需要 root**。
	//   · 节点模式（配了 MYNET_GATEWAYS）：要 ① 起 gnb（建 tun 需 CAP_NET_ADMIN）
	//     ② apply 写内核路由 ③ 读 gnb.map 拿 peer/流量 —— 而 gnb 以 root 跑、
	//     把 map 建成 0600 root:root，非 root 读到 `open ctl block error`
	//     且 gnb_ctl **仍然 exit 0**（CPI-58/CPI-63）。
	//
	// 不做 fail-fast：mynetd 的核心职责（WS + 拉 bundle + 心跳）确实不需要 root，
	// 整体拒启会打断合法的半特权部署，也会打断守护 V1-10 的 scripts/e2e/bundle_pull.sh
	// （它就是非 root 跑常驻 mynetd）。改为**高声警告 + 每个特权操作各自报明确的权限错**，
	// 生产部署走 deploy/systemd/mynetd.service（root）。
	if os.Geteuid() != 0 {
		if *console {
			logger.Info("console 模式非 root 运行（D-8/D-12：无 WS、无节点身份、不碰驱动）")
		} else if os.Getenv("MYNET_GATEWAYS") != "" {
			logger.Warn("节点模式下以非 root 运行 —— 隧道生命周期将无法自动管理",
				"后果1", "起不了 gnb（建 tun 需特权）：service start 会失败",
				"后果2", "写不了内核路由：apply 会失败，流量走默认路由不进隧道",
				"后果3", "读不到 gnb.map（0600 root:root）：心跳里 connected_peers/rx/tx 恒 0",
				"处置", "生产用 deploy/systemd/mynetd.service（root）；"+
					"手工跑则 sudo -E，或按 CPI-63 的三个备选方案之一处理")
		} else {
			// 未配 gateways 的纯本机模式：只有 localapi，不碰隧道，不该拿隧道的话术吓人
			logger.Info("非 root 运行（未配 MYNET_GATEWAYS，纯本机模式：只有 localapi，不管理隧道）")
		}
	}

	api := localapi.NewServer(localapi.Config{Listen: *listen})
	api.Ops = execMynet

	if err := api.Start(); err != nil {
		logger.Error("localapi 启动失败", "err", err)
		os.Exit(1)
	}
	logger.Info("localapi 已启动", "addr", api.Addr())

	if *console {
		// D-12：平台态经 gateway /v1/relay；无 WS、无驱动密钥
		waitSignal()
		_ = api.Close()
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// S3-1：心跳组装 + 命令分派 + WS 长连接。
	// ops 用 execMynetStd（直接给 stdout 字符串），省掉在 beat 里再拆 map。
	// ⚠️ 这里只**创建**不启动 —— 启动放在下面节点面客户端装配完之后，
	// 否则 `--once`（专门用来调试上报的模式）会在 reporter 还没绑上时就跑完，
	// 表现为「采了但没上报」，而这恰恰是最难查的一种静默失败。
	hb := &heartbeatLoop{ops: execMynetStd}

	// 启动拉取 bundle 的完成信号，给 `-once` 用（理由见下面 -once 分支）。
	// 声明在函数级：拉取在 MYNET_GATEWAYS 块内，而 -once 分支在块外。
	var bundlePullDone chan struct{}

	// WS → gateway（C-3 只带 X-Node-Id）；未配 gateways 则纯本机
	if gw := os.Getenv("MYNET_GATEWAYS"); gw != "" {
		gateways := strings.Split(gw, ",")
		nodeID := os.Getenv("MYNET_NODE_ID")

		// 节点面身份（C-2）：命令结果要签名后才能上报 back，
		// 拉取待执行命令也要签名。读不到 mynet.conf 时**明确告警**而不是静默降级——
		// 否则症状是「命令下发了但节点毫无反应」，比直接报错难查得多。
		var (
			onResult func(context.Context, commands.Result) ([]byte, error)
			pull     func(context.Context) ([]pendingCommand, error)
			bc       *backClient
		)
		confPath := mynetConfPath()
		id, idErr := loadNodeIdentity(confPath)
		if idErr != nil {
			logger.Warn("节点面未就绪，命令拉取与结果上报都不可用", "err", idErr)
		} else {
			if nodeID == "" {
				nodeID = id.nodeID
			}
			onResult = func(_ context.Context, res commands.Result) ([]byte, error) {
				return id.encodeResult(res)
			}
			// 节点面 HTTP 走 gateway 的 /v1/relay（back 对节点不可达）
			if c, err := newBackClient(gateways[0], id); err != nil {
				logger.Warn("节点面 HTTP 客户端不可用", "err", err)
			} else {
				bc = c
				pull = func(ctx context.Context) ([]pendingCommand, error) {
					resp, err := bc.pullPendingCommands(ctx)
					if err != nil {
						return nil, err
					}
					return resp.Commands, nil
				}
				// V4-2：心跳走**同一条**节点面通路（签名 + gateway /v1/relay）。
				// relay 白名单本来就含 /api/v1/monitor，不需要给 gateway 加新面。
				hb.setReporter(id.nodeID, c.reportHeartbeat)
			}
			logger.Info("节点面签名就绪", "node_id", id.nodeID, "conf", confPath)
		}

		// report 需要 cli.Send，而 cli 的 Handlers 又需要 dispatcher.Handle——
		// 用后绑闭包解开这个环：闭包只在事件真正到达时才求值，那时 cli 已就绪。
		var cli *transport.Client
		var report func(context.Context, commands.Result) error
		if id != nil {
			nodeIdent := id
			report = func(_ context.Context, res commands.Result) error {
				frame, err := nodeIdent.encodeResult(res)
				if err != nil {
					return err
				}
				if cli == nil {
					return fmt.Errorf("transport 尚未装配，无法上行结果")
				}
				return cli.Send(frame)
			}
		}

		dispatcher := newEventDispatcher(nodeID, execMynetStd, pull, report)

		// bundle 拉取（V1-10①）：mynetd 负责**拉并落盘**，执行器 mynet 只负责读。
		// 两边路径口径都是 shared.VarDir()，见 bundle.go 的注释。
		if bc != nil {
			store := newBundleStore()
			client := bc
			dispatcher.setBundleSyncer(func(ctx context.Context) (string, error) {
				return store.Sync(ctx, client.fetchBundle)
			})
			// 启动即拉一次。否则新装节点要等 back 广播 bundle_changed 才有配置，
			// 在那之前跑任何 refresh-* 都是「读 bundle 失败: no such file」。
			// 放 goroutine：拉取失败不该拖住 daemon 启动（back 暂时不可达是常态）。
			bundlePullDone = make(chan struct{})
			go func() {
				defer close(bundlePullDone)
				ctx, cancel := context.WithTimeout(context.Background(), bundleSyncTimeout)
				defer cancel()
				status, err := store.Sync(ctx, client.fetchBundle)
				if err != nil {
					logger.Warn("启动拉取 bundle 失败（bundle_changed 事件与下次启动会重试）",
						"err", err, "path", store.path)
					return
				}
				logger.Info("启动拉取 bundle 完成", "status", status, "path", store.path)
				// 启动时也要走一遍完整生命周期，不能只在 bundle_changed 时走。
				//
				// 实测（Phase 6 装成 systemd 服务后）：重启后 gnb 由它自己的单元拉起、
				// tun 接口也在，但 **/32 主机路由丢了** —— tun 重建时内核不会自动
				// 恢复此前 `ip route replace` 加进去的路由。症状与 CPI-62 同形：
				// 接口在、隧道在、ping 100% 丢包。
				//
				// `-once` 时跳过：那是验收用的一次性探针（只验「拉取 + 心跳」），
				// 让它顺手起隧道/改内核路由会污染被测机器的状态。
				if !*once {
					dispatcher.runBundleLifecycle()
				}
			}()
		}

		var err error
		cli, err = transport.New(transport.Config{
			Gateways: gateways,
			NodeID:   nodeID,
		}, transport.Handlers{
			OnCommand: func(_ context.Context, req commands.Request) commands.Result {
				return commands.Run(execMynetStd, req, nodeID)
			},
			OnEvent:  dispatcher.Handle,
			OnResult: onResult,
		}, func(f string, a ...any) { logger.Info(fmt.Sprintf(f, a...)) })
		if err != nil {
			logger.Error("transport 启动失败", "err", err)
		} else {
			cli.SetDialer(transport.WebSocketDialer)
			go func() { _ = cli.Run(ctx) }()
		}
	}

	// 启动心跳。放在节点面客户端装配**之后**（理由见上面 hb 创建处）：
	// reporter 绑好了再跑，`--once` 才能真的验到上报。
	if *once {
		hb.beat(ctx)
		// ⚠️ 必须等启动拉取结束再退出。启动拉取跑在 goroutine 里（上面），
		// 而 -once 做完心跳就 return —— 于是它可能在拉取完成前就退出。
		// 实测：同一条命令在两台机器上，一台 var/bundle.json 落盘了、
		// 另一台 var/ 全空，**两边退出码都是 0、日志里都没有错误** ——
		// 纯粹的静默失败，而 -once 恰恰是验收脚本用来当一次性探针的。
		// scripts/e2e/bundle_pull.sh 此前靠 `-once` 后 `sleep 1` 糊这个竞态，
		// 且它的 `before == after` 分支在「拉取根本没发生」时也会判过。
		if bundlePullDone != nil {
			select {
			case <-bundlePullDone:
			case <-time.After(bundleSyncTimeout):
				logger.Warn("--once: 等启动拉取超时，bundle 可能未落盘")
			}
		}
		// --once 的语义是「只探测一次心跳就退出」（见上面 flag 的说明）。
		// 此前它 beat 完仍会掉到 <-ctx.Done() 常驻不退，与说明不符 ——
		// 做全链路验证时只能 sleep + kill，脚本里没法当一次性探针用。
		// return 会触发 defer cancel()，WS 与 localapi 随之收尾。
		_ = api.Close()
		return
	}
	go hb.run(ctx)

	<-ctx.Done()
	_ = api.Close()
}

func waitSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
}

// execMynet 命令分派：桌面 exec mynet <子命令>；OpenWrt 构建可换 lua -e。
func execMynet(argv []string) (any, error) {
	bin := mynetBin()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := map[string]any{
		"argv":   argv,
		"stdout": stdout.String(),
		"stderr": stderr.String(),
	}
	if err != nil {
		out["error"] = err.Error()
		return out, err
	}
	return out, nil
}

func mynetBin() string {
	if v := os.Getenv("MYNET_BIN"); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		// 同目录 mynet
		cand := filepath.Join(filepath.Dir(exe), "mynet")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand
		}
	}
	return "mynet"
}

// heartbeatLoop 组装心跳（`mynet metrics --json`）并上报 back。
//
// ⚠️ 采集在**执行器**里（mynet 二进制），这里只 exec + 组装 + 发送 ——
// AGENTS.md §1.2：mynetd 是纯通信层，不含探测解析。
type heartbeatLoop struct {
	ops func(argv []string) (string, string, error)

	// reporter 与 nodeID 是**后绑**的：hb 在 main 里先创建（纯本机模式也要跑，
	// 保证 system.json 可用），而节点面客户端要等 MYNET_GATEWAYS 与密钥都就绪
	// 才有。用 mutex 保护 —— 绑的时候 goroutine 可能已经在跑，
	// 裸字段就是数据竞争（go test -race 会直接报）。
	mu       sync.Mutex
	nodeID   string
	reporter func(ctx context.Context, sendBody, signBody []byte) error
}

// setReporter 绑上上报通道。未绑时 beat 只采集不上报（纯本机模式）。
func (h *heartbeatLoop) setReporter(nodeID string, f func(context.Context, []byte, []byte) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nodeID = nodeID
	h.reporter = f
}

func (h *heartbeatLoop) getReporter() (string, func(context.Context, []byte, []byte) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nodeID, h.reporter
}

func (h *heartbeatLoop) run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	h.beat(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.beat(ctx)
		}
	}
}

func (h *heartbeatLoop) beat(ctx context.Context) {
	stdout, stderr, err := h.ops([]string{"metrics", "--json"})
	if err != nil {
		logger.Error("heartbeat 采集失败", "err", err, "stderr", strings.TrimSpace(stderr))
		return
	}
	// 本机态也保证 system.json 可用
	if _, err := shared.ReadSnapshot(shared.DefaultSnapshotPath()); err != nil {
		logger.Warn("system.json 缺失，建议运行 mynet detector")
	}

	// 上报（V4-2）。此前这里是 `_ = out` —— 采完就丢，于是「心跳」这个词在
	// mynetd 里只对应 WS 上一个 `{"t":"ping"}`，back 的 monitor/heartbeat
	// 从来没收到过真指标。
	nodeID, report := h.getReporter()
	if report == nil {
		return // 纯本机模式（未配 MYNET_GATEWAYS，或节点面密钥未就绪）
	}
	if strings.TrimSpace(stdout) == "" {
		logger.Warn("heartbeat metrics 无输出，跳过本次上报", "node_id", nodeID)
		return
	}
	sendBody, signBody, err := heartbeatBodies(nodeID, stdout)
	if err != nil {
		logger.Error("heartbeat 组装失败", "node_id", nodeID, "err", err)
		return
	}
	if err := report(ctx, sendBody, signBody); err != nil {
		// 上报失败**不该**让循环退出（下一拍再试），但必须说出来 ——
		// 静默失败会让「节点显示在线却没有数据」这种状态查很久。
		logger.Error("heartbeat 上报失败", "node_id", nodeID, "err", err)
		return
	}
	// 成功走 Debug：30s 一次，Info 会把日志刷满，而「没消息」本身就是好消息。
	// 排查时 MYNET_LOG_LEVEL=debug 就能看到每一拍。
	logger.Debug("heartbeat 已上报", "node_id", nodeID, "bytes", len(sendBody))
}

// execMynetStd 适配 commands.Executor 签名
func execMynetStd(argv []string) (string, string, error) {
	out, err := execMynet(argv)
	if err != nil {
		return "", "", err
	}
	m, _ := out.(map[string]any)
	stdout, _ := m["stdout"].(string)
	stderr, _ := m["stderr"].(string)
	return stdout, stderr, nil
}
