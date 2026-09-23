// mynetd — 纯通信层常驻进程。
//
// D-8：console 模式**不**强制 root（旧实现 os.Geteuid()!=0 即 Fatal）。
// 需要写路由/防火墙的动作全在执行器 `mynet`，由调用方自行提权。
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
	"syscall"
	"time"

	"github.com/mynet-club/mynet_agent-V2/mynetd"
	"github.com/mynet-club/mynet_agent-V2/mynetd/commands"
	"github.com/mynet-club/mynet_agent-V2/mynetd/localapi"
	"github.com/mynet-club/mynet_agent-V2/mynetd/transport"
	"github.com/mynet-club/mynet_agent-V2/shared"
)

func main() {
	var (
		listen  = flag.String("listen", mynetd.DefaultLocalAPI, "本机 API 监听")
		console = flag.Bool("console", false, "console 模式：无 WS、无节点身份（D-12）")
		once    = flag.Bool("once", false, "只探测一次心跳就退出（调试）")
	)
	flag.Parse()

	// D-8：去掉强制 root。系统能力在 mynet，由用户按需 sudo。
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "提示: mynetd 无需 root；需要写路由/防火墙时请 sudo mynet apply …")
	}

	api := localapi.NewServer(localapi.Config{Listen: *listen})
	api.Ops = execMynet

	if err := api.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "localapi 启动失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("mynetd localapi → http://%s/\n", api.Addr())

	if *console {
		// D-12：平台态经 gateway /v1/relay；无 WS、无驱动密钥
		waitSignal()
		_ = api.Close()
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// S3-1：心跳组装 + 命令分派 + WS 长连接
	hb := &heartbeatLoop{ops: execMynet}
	if *once {
		hb.beat()
	} else {
		go hb.run(ctx)
	}

	// WS → gateway（C-3 只带 X-Node-Id）；未配 gateways 则纯本机
	if gw := os.Getenv("MYNET_GATEWAYS"); gw != "" {
		nodeID := os.Getenv("MYNET_NODE_ID")
		cli, err := transport.New(transport.Config{
			Gateways: strings.Split(gw, ","),
			NodeID:   nodeID,
		}, transport.Handlers{
			OnCommand: func(_ context.Context, req commands.Request) commands.Result {
				return commands.Run(execMynetStd, req, nodeID)
			},
		}, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
		if err != nil {
			fmt.Fprintf(os.Stderr, "transport: %v\n", err)
		} else {
			cli.SetDialer(transport.WebSocketDialer)
			go func() { _ = cli.Run(ctx) }()
		}
	}

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

// heartbeatLoop 组装心跳数据（metrics --json）并准备上报。
// WS 上报在 transport 接入后挂接；当前先把组装跑通。
type heartbeatLoop struct {
	ops func(argv []string) (any, error)
}

func (h *heartbeatLoop) run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	h.beat()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.beat()
		}
	}
}

func (h *heartbeatLoop) beat() {
	out, err := h.ops([]string{"metrics", "--json"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "heartbeat metrics: %v\n", err)
		return
	}
	_ = out
	// 本机态也保证 system.json 可用
	if _, err := shared.ReadSnapshot(shared.DefaultSnapshotPath()); err != nil {
		fmt.Fprintln(os.Stderr, "system.json 缺失，建议运行 mynet detector")
	}
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
