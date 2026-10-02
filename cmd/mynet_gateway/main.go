// mynet_gateway — 边缘网关常驻进程（节点面 WS + 用户面 /v1/relay）。
//
// **gateway 不鉴权**（C-3）：纯透传 + 连接状态表。连接层的节点标识只做寻址与
// presence（门牌号），不是授权；鉴权一律归 back（用户面 cookie/会话，节点面 DH+HMAC）。
//
// 但「不鉴权节点」不等于「谁都能操作网关」。/v1/notify 是 back → gateway 的推送入口，
// 由 X-Internal-Token 保护（**运维口令，不是节点鉴权**）。handleNotify 的判断是
// `if s.InternalToken != "" && …`——空口令即完全不校验，任何人都能向节点推事件。
// 因此这里按监听地址 fail fast：非回环监听必须配口令。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mynet-club/mynet_agent/gateway"
	"github.com/mynet-club/mynet_agent/shared/logger"
	"github.com/mynet-club/mynet_agent/shared/utils"
)

// DefaultListen 网关默认监听（AGENTS.md §1.5：gateway 默认 8710）。
const DefaultListen = ":8710"

// shutdownTimeout 优雅退出上限：给在途请求留时间，但不无限等。
const shutdownTimeout = 15 * time.Second

func main() {
	var (
		listen        = flag.String("listen", envOr("MYNET_GATEWAY_LISTEN", DefaultListen), "监听地址")
		upstream      = flag.String("upstream", envOr("MYNET_GATEWAY_UPSTREAM", ""), "back 内网地址（部署配置，不下发）")
		internalToken = flag.String("internal-token", envOr("MYNET_GATEWAY_INTERNAL_TOKEN", ""), "back ↔ gateway 运维口令（非节点鉴权）")
		version       = flag.Bool("version", false, "打印版本并退出")
	)
	flag.Parse()

	// 日志装配（V4-4）：默认 info + text + stderr；MYNET_LOG_FORMAT=json 切结构化。
	// systemd 单元用 EnvironmentFile 注入（deploy/systemd/mynet_gateway.service）。
	// 必须在任何日志输出之前 —— 否则同一次运行里会混两种格式。
	if err := logger.InitFromEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "logger: %v\n", err)
		os.Exit(2)
	}
	defer logger.Close()

	if *version {
		fmt.Printf("mynet_gateway %s\n", utils.AppVersion)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *listen, *upstream, *internalToken); err != nil {
		logger.Error("gateway 异常退出", "err", err)
		os.Exit(1)
	}
}

// run 起服务并阻塞到 ctx 取消或监听出错。ctx 由调用方持有，便于测试注入取消。
func run(ctx context.Context, listen, upstream, internalToken string) error {
	if err := validateConfig(listen, internalToken); err != nil {
		return err
	}

	srv := gateway.NewServer(upstream, internalToken)
	srv.SetLogger(func(format string, args ...any) {
		logger.Info(fmt.Sprintf(format, args...))
	})

	// presence 接线（V4-5）。配了 Redis 地址就用 Redis（跨实例同步 online/
	// 并广播事件帧），没配就保持 Registry 默认的内存实现（单实例）。
	//
	// 降级由 NewRedisPresence 自己负责：addr 为空或 Ping 失败**直接返回内存
	// 实现**，不返回 error —— 所以这里拿不到 err 不是「没配 Redis 就不管了」，
	// 而是「已经退化到内存了」。生产侧要跨实例就必须给 addr，
	// 见 deploy/gateway.env.example。
	var presence gateway.PresenceStore
	if addr := strings.TrimSpace(os.Getenv("MYNET_GATEWAY_REDIS_ADDR")); addr != "" {
		db, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("MYNET_GATEWAY_REDIS_DB")))
		ps, err := gateway.NewRedisPresence(addr, os.Getenv("MYNET_GATEWAY_REDIS_PASSWORD"), db)
		if err != nil {
			// 当前实现不走这里，保留分支以防将来变严格（宁可告警也别静默）
			logger.Warn("presence 初始化失败，退回内存实现", "err", err, "addr", addr)
		} else if _, ok := ps.(*gateway.MemoryPresence); ok {
			logger.Warn("配置了 Redis 地址但连不上，presence 已退回内存（跨实例失效）", "addr", addr)
		} else {
			presence = ps
			srv.Registry().SetPresence(ps)
			logger.Info("presence 已接 Redis", "addr", addr)
		}
	} else {
		logger.Info("未配 MYNET_GATEWAY_REDIS_ADDR，presence 用内存实现（仅单实例可用）")
	}
	defer func() {
		if presence != nil {
			_ = presence.Close()
		}
	}()

	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// 先监听再服务，端口占用能立刻报错而不是静默失败。
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", listen, err)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	logger.Info("gateway 已启动",
		"listen", listen, "upstream", displayUpstream(upstream), "conns", srv.Registry().Count())

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("服务异常退出: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	logger.Info("收到退出信号，开始优雅关闭")
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		_ = httpSrv.Close()
		return fmt.Errorf("优雅关闭超时，已强制关闭: %w", err)
	}
	logger.Info("已关闭", "conns_at_exit", srv.Registry().Count())
	return nil
}

// validateConfig 启动前校验。空口令 + 对外监听是真实安全洞（/v1/notify 无校验），
// 必须 fail fast 而不是打个警告继续跑。
func validateConfig(listen, internalToken string) error {
	if isLoopback(listen) || internalToken != "" {
		return nil
	}
	return fmt.Errorf("非回环监听 %s 必须配置 -internal-token（或 MYNET_GATEWAY_INTERNAL_TOKEN）："+
		"空口令下 /v1/notify 完全不校验，任何人都能向节点推送事件", listen)
}

// isLoopback 判断监听地址是否只绑回环。空 host（":8710"、"0.0.0.0:8710"）视为对外。
func isLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host == "localhost"
	}
	return ip.IsLoopback()
}

func displayUpstream(u string) string {
	if u == "" {
		return "<未配置>"
	}
	return u
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
