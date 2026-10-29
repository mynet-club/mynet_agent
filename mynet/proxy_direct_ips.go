package mynet

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 直连 IP 豁免：这些 IP/网段永不钉进隧道。
// 场景：白名单域名与普通应用共用 CDN IP（实测 api.deepseek.com 与 MiMo 共用
// 123.125.246.121），/32 钉扎按 IP 生效，会把普通应用一起带进隧道。
func hookDirectIPsPath() string {
	return filepath.Join(varDir(), "proxy", "dns-hook-direct-ips.json")
}

type hookDirectIPFile struct {
	CIDRs []string `json:"cidrs"`
}

func loadHookDirectIPs() []string {
	raw, err := os.ReadFile(hookDirectIPsPath())
	if err != nil {
		return nil
	}
	var f hookDirectIPFile
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return f.CIDRs
}

func saveHookDirectIPs(list []string) error {
	p := hookDirectIPsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, c := range list {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	sort.Strings(out)
	raw, err := json.Marshal(hookDirectIPFile{CIDRs: out})
	if err != nil {
		return err
	}
	if err := os.WriteFile(p+".tmp", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

func directIPExempted(ip string) bool {
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	for _, c := range loadHookDirectIPs() {
		if _, n, err := net.ParseCIDR(normalizeCIDR(c)); err == nil && n.Contains(addr) {
			return true
		}
	}
	return false
}

func normalizeCIDR(c string) string {
	if strings.Contains(c, "/") {
		return c
	}
	if ip := net.ParseIP(c); ip != nil {
		return ip.String() + "/32"
	}
	return c
}

// cmdProxyDNSHookDirectIPs —— direct-ip add|del|list [CIDR]
func cmdProxyDNSHookDirectIPs(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: mynet proxy dns-hook direct-ip add|del|list [CIDR]")
		return 2
	}
	list := loadHookDirectIPs()
	switch args[0] {
	case "list":
		for _, c := range list {
			fmt.Println(c)
		}
		return 0
	case "add":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "用法: direct-ip add <CIDR|IP>")
			return 2
		}
		c := normalizeCIDR(args[1])
		if _, _, err := net.ParseCIDR(c); err != nil {
			fmt.Fprintln(os.Stderr, "非法 CIDR:", args[1])
			return 2
		}
		list = append(list, c)
	case "del":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "用法: direct-ip del <CIDR|IP>")
			return 2
		}
		c := normalizeCIDR(args[1])
		next := make([]string, 0, len(list))
		for _, x := range list {
			if x != c {
				next = append(next, x)
			}
		}
		list = next
	default:
		fmt.Fprintln(os.Stderr, "未知子命令:", args[0])
		return 2
	}
	if err := saveHookDirectIPs(list); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("direct-ip: %v\n", loadHookDirectIPs())
	return 0
}
