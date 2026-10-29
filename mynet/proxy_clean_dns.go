package mynet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 干净 DNS（clean_public 源）——**必须固定钉进隧道**：
// 查询这些解析器时若走直连，GFW 会投毒（t66y.com 实测）。因此：
//  1. 其 IP 以 /32 **永久钉入** utun（不走域名租约、不被共享 IP 解钉）；
//  2. 解析链 clean_public 源的值 = 本清单。
//
// SSOT 是 ctl `dns-clean.txt`（发布 → /plugins/mp/dns-clean.txt，agent 定期
// refreshCleanDNSList 拉取落 dns_clean.json）。一份两用：既是 clean_public
// 解析源值，也是「必须钉 /32 进隧道」的 IP 集——进解析链的 IP 必然在隧道里。
const cleanDNSListVersionKey = "pc-clean-dns-v1"

// builtinCleanDNSList 内置兜底，与 ctl dns-clean.txt 同步（发版时核对）。
// 仅在 ctl 拉不到/无缓存时生效；「必须固定钉入路由」不允许因 ctl 不可达而少钉。
var builtinCleanDNSList = []string{
	"1.1.1.1", "1.0.0.1",
	"8.8.8.8", "8.8.4.4",
	"9.9.9.9", "149.112.112.112",
	"208.67.222.222", "208.67.220.220",
}

func cleanDNSListPath() string {
	return filepath.Join(ProxyStateDir(), "dns_clean.json")
}

type cleanDNSListFile struct {
	IPs []string `json:"ips"`
}

// loadCleanDNSList 读本机缓存的干净 DNS 列表；空则回落内置全量兜底。
func loadCleanDNSList() []string {
	raw, err := os.ReadFile(cleanDNSListPath())
	if err != nil {
		// 无缓存文件：内置兜底
		return normalizeDNSList(builtinCleanDNSList)
	}
	var f cleanDNSListFile
	if json.Unmarshal(raw, &f) != nil {
		return normalizeDNSList(builtinCleanDNSList)
	}
	// 文件在场即权威：空表 = 显式不钉任何解析器
	return normalizeDNSList(f.IPs)
}

// cleanDNSCleanupList 遗留清理用的超集：内置 ∪ 缓存。
// 清理要摘除「历史上可能钉过」的一切 /32，清单缩小也不许留残钉，故取超集；
// 钉入路径仍用 loadCleanDNSList（当前权威），两路不对称是有意的。
func cleanDNSCleanupList() []string {
	return normalizeDNSList(append(append([]string{}, builtinCleanDNSList...), loadCleanDNSList()...))
}

func saveCleanDNSList(list []string) error {
	p := cleanDNSListPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(cleanDNSListFile{IPs: normalizeDNSList(list)})
	if err != nil {
		return err
	}
	if err := os.WriteFile(p+".tmp", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// refreshCleanDNSList 从 ctl 拉 dns-clean.txt；失败保留旧表。
func refreshCleanDNSList(ctx context.Context, client *http.Client, base string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"dns-clean.txt", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ctl dns-clean.txt: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return err
	}
	var ips []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" {
			continue
		}
		ip := net.ParseIP(line)
		if ip == nil || ip.To4() == nil {
			continue
		}
		ips = append(ips, ip.String())
	}
	if len(ips) == 0 {
		return fmt.Errorf("ctl dns-clean.txt: empty")
	}
	sort.Strings(ips)
	return saveCleanDNSList(ips)
}

// isCleanDNSIP 是否属于「必须永久钉进隧道」的干净 DNS。
func isCleanDNSIP(ip string) bool {
	for _, c := range loadCleanDNSList() {
		if c == ip {
			return true
		}
	}
	return false
}
