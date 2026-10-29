package mynet

import (
	"context"
	"crypto/sha256"
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
	version := fmt.Sprintf("%x", sha256.Sum256([]byte("pc-lists-v2\n"+strings.Join(proxied, "\n")+"\n--\n"+strings.Join(direct, "\n"))))

	current, err := config.LoadProxyDomainBaseline(ProxyStateDir())
	if err != nil {
		return err
	}
	if current != nil && current.Version == version {
		return nil
	}
	return config.SaveProxyDomainBaseline(ProxyStateDir(), &config.ProxyDomainBaseline{Proxied: proxied, Direct: direct, Version: version})
}

func (s *dnsHookServer) updateBaseline(ctx context.Context) {
	client := &http.Client{Timeout: 10 * time.Second}
	if err := refreshProxyDomainBaseline(ctx, client, proxyDomainBaselineURL()); err != nil {
		hookLogf("ctl domains: %v", err)
	}
	if err := refreshDNSBlockBaseline(ctx, client, proxyDomainBaselineURL()); err != nil {
		hookLogf("ctl block domains: %v", err)
	}
	// 干净 DNS 清单（SSOT=ctl dns-clean.txt）：解析源值 + 永久钉隧道同一份
	if err := refreshCleanDNSList(ctx, client, proxyDomainBaselineURL()); err != nil {
		hookLogf("ctl clean dns: %v", err)
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
		out := map[string]any{"proxied": []string{}, "direct": []string{}, "version": ""}
		if base != nil {
			if base.Proxied == nil {
				base.Proxied = []string{}
			}
			if base.Direct == nil {
				base.Direct = []string{}
			}
			out["proxied"] = base.Proxied
			out["direct"] = base.Direct
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
		out := map[string]any{"proxied": []string{}, "direct": []string{}, "version": "", "refreshed": true}
		if base != nil {
			if base.Proxied == nil {
				base.Proxied = []string{}
			}
			if base.Direct == nil {
				base.Direct = []string{}
			}
			out["proxied"] = base.Proxied
			out["direct"] = base.Direct
			out["version"] = base.Version
		}
		out["clean_dns"] = loadCleanDNSList()
		return printJSON(out)
	default:
		fmt.Fprintf(os.Stderr, "domains: 未知子命令 %q\n", args[0])
		return 2
	}
}
