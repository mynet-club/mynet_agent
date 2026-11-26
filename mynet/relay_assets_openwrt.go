//go:build linux && openwrt

package mynet

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// OpenWrt 两跳中转执行面：脚本落位 + ACL 派生文件 + UCI include 登记。
// 脚本本体 embed 在二进制里（units/openwrt_2hop_hook.sh），重装/升级不丢。

const relayHookRelDir = "scripts/plugin/2hop"
const relayHookName = "post_start.sh"

func relayHookPath(home string) string {
	return filepath.Join(home, relayHookRelDir, relayHookName)
}

func relayACLPath(home string) string {
	return filepath.Join(home, "var", "2hop_acl.json")
}

// EnsureRelayAssets 落地/清理两跳中转资产（幂等）。
//
// enabled=true：写钩子脚本(0755) + 派生 ACL(0600) + 登记 UCI include + 立即执行一次。
// enabled=false：摘 UCI 段 + 执行一次 DISABLE 清理（脚本不存在则静默）。
func EnsureRelayAssets(home string, acl relayACL) error {
	path := relayHookPath(home)
	if !acl.Enabled {
		if err := unregisterRelayInclude(); err != nil {
			return err
		}
		_ = os.Remove(relayACLPath(home))
		if _, err := os.Stat(path); err == nil {
			if _, err := exec.LookPath("iptables"); err == nil {
				return runRelayHook(home, acl.Dev, "MYNET_2HOP_DISABLE=1")
			}
		}
		return nil
	}
	profile, err := openwrtFirewallProfile()
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("iptables"); err != nil {
		return fmt.Errorf("2hop 需要 iptables（当前防火墙 %s 缺少 iptables 命令）", profile) // GUARD-OK 2hop-runtime（仅探测命令存在性）
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("建 2hop 目录: %w", err)
	}
	if err := os.WriteFile(path, []byte(OpenWrt2HopHookScript()), 0o755); err != nil {
		return fmt.Errorf("写 2hop 钩子: %w", err)
	}
	body, err := json.MarshalIndent(map[string]any{
		"schema":      1,
		"mode":        acl.Mode,
		"dev":         acl.Dev,
		"ovl_cidrs":   acl.OverlayCIDRs,
		"allow_cidrs": acl.Allow,
		"deny_cidrs":  acl.Deny,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(relayACLPath(home)), 0o700); err != nil {
		return fmt.Errorf("建 var 目录: %w", err)
	}
	if err := os.WriteFile(relayACLPath(home), body, 0o600); err != nil {
		return fmt.Errorf("写 2hop ACL: %w", err)
	}
	if err := registerRelayInclude(path); err != nil {
		return err
	}
	return runRelayHook(home, acl.Dev, "")
}

func runRelayHook(home, dev, extraEnv string) error {
	cmd := exec.Command("sh", relayHookPath(home))
	cmd.Env = append(os.Environ(), "MYNET_HOME="+home)
	if dev != "" {
		cmd.Env = append(cmd.Env, "MYNET_2HOP_DEV="+dev)
	}
	if extraEnv != "" {
		cmd.Env = append(cmd.Env, extraEnv)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("2hop 钩子执行: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func registerRelayInclude(path string) error {
	steps := [][]string{
		{"-q", "get", "firewall.mynet_2hop_include"},
	}
	if err := uciArgs(steps[0]...).Run(); err != nil {
		if err := uciArgs("-q", "set", "firewall.mynet_2hop_include=include").Run(); err != nil {
			return fmt.Errorf("uci 建 2hop include 段: %w", err)
		}
	}
	for _, kv := range [][2]string{
		{"firewall.mynet_2hop_include.enabled", "1"},
		{"firewall.mynet_2hop_include.type", "script"},
		{"firewall.mynet_2hop_include.reload", "1"},
		{"firewall.mynet_2hop_include.path", path},
	} {
		if err := uciArgs("set", kv[0]+"="+kv[1]).Run(); err != nil {
			return fmt.Errorf("uci set %s: %w", kv[0], err)
		}
	}
	if err := uciArgs("commit", "firewall").Run(); err != nil {
		return fmt.Errorf("uci commit firewall: %w", err)
	}
	return nil
}

func unregisterRelayInclude() error {
	_ = uciArgs("-q", "delete", "firewall.mynet_2hop_include").Run()
	if err := uciArgs("commit", "firewall").Run(); err != nil {
		return fmt.Errorf("uci commit firewall: %w", err)
	}
	return nil
}

func uciArgs(args ...string) *exec.Cmd {
	return exec.Command("uci", args...)
}
