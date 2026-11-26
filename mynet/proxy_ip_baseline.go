package mynet

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mynet-club/mynet_agent/shared/domain"
)

// parseProxyIPList 解析 ctl 发布的预设代理 IPv4/CIDR 清单（proxy-ips.txt）。
// 支持 `#` 注释与空行；任一非法条目、IPv6 或 < /8 过宽网段均整表报错，保留旧缓存。
func parseProxyIPList(raw string) ([]string, error) {
	var items []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" {
			continue
		}
		items = append(items, line)
	}
	kept, _, err := domain.NormalizeIPv4CIDRs(items)
	if err != nil {
		return nil, err
	}
	return kept, nil
}

// fetchOptionalProxyIPList 从 ctl 拉取可选的 proxy-ips.txt 预设清单。
// 404 视为空表（兼容尚未发布 proxy-ips.txt 的旧版 ctl）；其余错误透传以便保留旧缓存。
func fetchOptionalProxyIPList(ctx context.Context, client *http.Client, base string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"proxy-ips.txt", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ctl proxy-ips.txt: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1024*1024 {
		return nil, fmt.Errorf("ctl proxy-ips.txt exceeds 1 MiB")
	}
	return parseProxyIPList(string(raw))
}
