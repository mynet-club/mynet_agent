package mynet

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mynet-club/mynet_agent/shared/config"
)

// 与升级分发共用 ctl 基址；域名基线按小时更新，不在 DNS 查询路径下载。
func proxyDomainBaselineURL() string {
	base := os.Getenv("MYNET_APPS_INDEX")
	if base == "" {
		base = "https://ctl.mynet.club"
	}
	return strings.TrimRight(base, "/") + "/d/plugins/mp/"
}

func parseProxyDomainList(raw string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" {
			continue
		}
		name := normalizeDomainName(line)
		suffix := strings.HasPrefix(line, "*.") || strings.HasPrefix(line, ".")
		if name == "" || strings.ContainsAny(name, " /:<>&\t*") || (!suffix && !strings.Contains(name, ".")) {
			return nil, fmt.Errorf("invalid domain entry %q", line)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

func refreshProxyDomainBaseline(ctx context.Context, client *http.Client, base string) error {
	// 三名单语义：拦截 > 直连 > 加速 > 放行。
	// 云端预定义：proxy-domains.txt（加速）+ direct-domains.txt（直连）；
	// 拦截基线走 dns_block_baseline（ad/malware）。
	fetchList := func(name string) ([]string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+name, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && name == "direct-domains.txt" {
			// 云端未发布直连表时不算失败：空表=无预定义直连，本地 CustomDirect 仍有效。
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("ctl %s: HTTP %d", name, resp.StatusCode)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
		if err != nil {
			return nil, err
		}
		if len(raw) > 1024*1024 {
			return nil, fmt.Errorf("ctl domain list exceeds 1 MiB")
		}
		return parseProxyDomainList(string(raw))
	}
	proxied, err := fetchList("proxy-domains.txt")
	if err != nil {
		return err
	}
	direct, err := fetchList("direct-domains.txt")
	if err != nil {
		return err
	}
	proxyIPs, err := fetchOptionalProxyIPList(ctx, client, base)
	if err != nil {
		return err
	}
	hashInput := "pc-lists-v2\n" + strings.Join(proxied, "\n") + "\n--\n" + strings.Join(direct, "\n")
	if len(proxyIPs) > 0 {
		hashInput += "\n--ips--\n" + strings.Join(proxyIPs, "\n")
	}
	version := fmt.Sprintf("%x", sha256.Sum256([]byte(hashInput)))

	current, err := config.LoadProxyDomainBaseline(ProxyStateDir())
	if err != nil {
		return err
	}
	if current != nil && current.Version == version {
		return nil
	}
	return config.SaveProxyDomainBaseline(ProxyStateDir(), &config.ProxyDomainBaseline{Proxied: proxied, Direct: direct, ProxyIPs: proxyIPs, Version: version})
}

func (s *dnsHookServer) updateBaseline(ctx context.Context) error {
	client := &http.Client{Timeout: 10 * time.Second}
	var errs []error
	if err := refreshProxyDomainBaseline(ctx, client, proxyDomainBaselineURL()); err != nil {
		hookLogf("ctl domains: %v", err)
		errs = append(errs, err)
	}
	if err := refreshDNSBlockBaseline(ctx, client, proxyDomainBaselineURL()); err != nil {
		hookLogf("ctl block domains: %v", err)
		errs = append(errs, err)
	}
	// 干净 DNS 清单（SSOT=ctl dns-clean.txt）：解析源值 + 永久钉隧道同一份
	if err := refreshCleanDNSList(ctx, client, proxyDomainBaselineURL()); err != nil {
		hookLogf("ctl clean dns: %v", err)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// baselineRetryDelay 拉取失败后的退避间隔：retryBase 起倍增、retryCap 封顶。
func baselineRetryDelay(failures int, retryBase, retryCap time.Duration) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := retryBase
	for i := 1; i < failures && d < retryCap; i++ {
		d *= 2
	}
	if d > retryCap {
		d = retryCap
	}
	return d
}

// runBaselineRefreshLoop 云端清单后台刷新循环（dns-hook worker 内跑）。
//
// 合同（2026-11-18 健壮性收口）：拉取失败/超时只记日志并退避重试，沿用已落盘
// 清单继续服务——不得退出、不得打断 DNS 监听。不可恢复的致命错误（端口被占、
// 配置非法）在 runDNSHookServer 启动检查里 fail loud，不在本循环。
//
// 参数零值取生产默认：steady=1h（与旧 hourly ticker 一致），失败退避 30s 起
// 倍增、15min 封顶。onUpdated 在每次成功后调用（热更新钩子，可为 nil）。
func runBaselineRefreshLoop(ctx context.Context, refresh func(context.Context) error, onUpdated func(), steady, retryBase, retryCap time.Duration) {
	if steady <= 0 {
		steady = time.Hour
	}
	if retryBase <= 0 {
		retryBase = 30 * time.Second
	}
	if retryCap <= 0 {
		retryCap = 15 * time.Minute
	}
	failures := 0
	for {
		err := refresh(ctx)
		var wait time.Duration
		if err != nil {
			failures++
			wait = baselineRetryDelay(failures, retryBase, retryCap)
			hookLogf("baseline refresh failed (attempt %d, cached lists stay active), retry in %s", failures, wait)
		} else {
			if failures > 0 {
				hookLogf("baseline refresh recovered after %d failed attempts", failures)
			}
			failures = 0
			wait = steady
			if onUpdated != nil {
				onUpdated()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// cmdProxyDNSHookDomains —— 云端域名基线只读查看 / 手动刷新。
// localapi 经 Ops 调这里；HTTP 下载留在执行器（mynetd 纯通信，B2）。
func cmdProxyDNSHookDomains(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet proxy dns-hook domains status|refresh")
		return 2
	}
	switch args[0] {
	case "status":
		base, err := config.LoadProxyDomainBaseline(ProxyStateDir())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		out := map[string]any{"proxied": []string{}, "direct": []string{}, "proxy_ips": []string{}, "version": ""}
		if base != nil {
			if base.Proxied == nil {
				base.Proxied = []string{}
			}
			if base.Direct == nil {
				base.Direct = []string{}
			}
			if base.ProxyIPs == nil {
				base.ProxyIPs = []string{}
			}
			out["proxied"] = base.Proxied
			out["direct"] = base.Direct
			out["proxy_ips"] = base.ProxyIPs
			out["version"] = base.Version
		}
		return printJSON(out)
	case "refresh":
		client := &http.Client{Timeout: 15 * time.Second}
		if err := refreshProxyDomainBaseline(context.Background(), client, proxyDomainBaselineURL()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		// 干净 DNS 清单与域名基线同族：手动刷新一并拉取
		if err := refreshCleanDNSList(context.Background(), client, proxyDomainBaselineURL()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		base, err := config.LoadProxyDomainBaseline(ProxyStateDir())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		out := map[string]any{"proxied": []string{}, "direct": []string{}, "proxy_ips": []string{}, "version": "", "refreshed": true}
		if base != nil {
			if base.Proxied == nil {
				base.Proxied = []string{}
			}
			if base.Direct == nil {
				base.Direct = []string{}
			}
			if base.ProxyIPs == nil {
				base.ProxyIPs = []string{}
			}
			out["proxied"] = base.Proxied
			out["direct"] = base.Direct
			out["proxy_ips"] = base.ProxyIPs
			out["version"] = base.Version
		}
		out["clean_dns"] = loadCleanDNSList()
		return printJSON(out)
	default:
		fmt.Fprintf(os.Stderr, "domains: 未知子命令 %q\n", args[0])
		return 2
	}
}
