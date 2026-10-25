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
	// PC 白名单加速：唯一基础清单是代理白名单，未命中默认直连。
	const name = "proxy-domains.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+name, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ctl %s: HTTP %d", name, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return err
	}
	if len(raw) > 1024*1024 {
		return fmt.Errorf("ctl domain list exceeds 1 MiB")
	}
	proxied, err := parseProxyDomainList(string(raw))
	if err != nil {
		return fmt.Errorf("ctl %s: %w", name, err)
	}
	version := fmt.Sprintf("%x", sha256.Sum256([]byte("pc-whitelist-v1\n"+strings.Join(proxied, "\n"))))

	current, err := config.LoadProxyDomainBaseline(ProxyStateDir())
	if err != nil {
		return err
	}
	if current != nil && current.Version == version {
		return nil
	}
	return config.SaveProxyDomainBaseline(ProxyStateDir(), &config.ProxyDomainBaseline{Proxied: proxied, Version: version})
}

func (s *dnsHookServer) updateBaseline(ctx context.Context) {
	client := &http.Client{Timeout: 10 * time.Second}
	if err := refreshProxyDomainBaseline(ctx, client, proxyDomainBaselineURL()); err != nil {
		hookLogf("ctl domains: %v", err)
	}
	if err := refreshDNSBlockBaseline(ctx, client, proxyDomainBaselineURL()); err != nil {
		hookLogf("ctl block domains: %v", err)
	}
}
