package mynet

// OpenWrt 防火墙持久化物（firewall.mynet.include）的内容生成 —— 平台无关，
// 执行面（写文件/UCI）见 openwrt_firewall_persist.go（仅 OpenWrt）。
//
// 持久化机制必须保留（2026-10-12 用户裁定）：fw4 reload/restart 会清掉
// `nft add` 手写规则，include 脚本在 fw4 起来后被调用重放。
//
// dev 安全契约：一律先过校验（`[A-Za-z0-9._-]{1,15}`），不含空格/引号/分号
// 才允许拼进 sh/脚本内容。

import (
	"fmt"
	"path/filepath"
	"strings"
)

// FirewallIncludeName include 脚本文件名（UCI mynet_include 指向处）。
const FirewallIncludeName = "firewall.mynet.include"

// FirewallIncludePath include 脚本路径（UCI mynet_include 指向处）。
func FirewallIncludePath(home string) string {
	return filepath.Join(home, "scripts", FirewallIncludeName)
}

// FirewallIncludeScript 生成 include 脚本内容（纯函数，可测）。
//
// 重放范围 = UCI 承载不了的 nft 手写规则：NAT masq（oif/iif 两向，与旧
// firewall.mynet.include 语义一致）。幂等：先查后加，重复执行不堆规则。
func FirewallIncludeScript(tunDev string) (string, error) {
	if err := validateTunDeviceName(tunDev); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# mynet firewall include —— 由 mynet 生成，勿手改。\n")
	b.WriteString("# 职责：fw4 reload/restart 后重放 mynet 手写的 NAT 规则。\n")
	b.WriteString("# 丢了这个文件 = 防火墙一重启规则就丢（持久化机制必须保留）。\n")
	b.WriteString("# 接口名与 UCI zone device 同源；变更由 `mynet firewall sync` 对齐。\n")
	fmt.Fprintf(&b, "DEV=%q\n", tunDev)
	b.WriteString("[ -n \"$DEV\" ] || exit 0\n")
	b.WriteString("command -v nft >/dev/null 2>&1 || exit 0\n\n")
	b.WriteString("# NAT masq 双向（幂等：先查后加）\n")
	b.WriteString("nft list table ip nat >/dev/null 2>&1 || nft add table ip nat\n")
	b.WriteString("nft list chain ip nat POSTROUTING >/dev/null 2>&1 || " +
		"nft add chain ip nat POSTROUTING '{ type nat hook postrouting priority srcnat; }'\n")
	b.WriteString("nft list chain ip nat POSTROUTING 2>/dev/null | grep -q \"oifname \\\"$DEV\\\" masquerade\" || " +
		"nft add rule ip nat POSTROUTING oifname \"$DEV\" masquerade\n")
	b.WriteString("nft list chain ip nat POSTROUTING 2>/dev/null | grep -q \"iifname \\\"$DEV\\\" masquerade\" || " +
		"nft add rule ip nat POSTROUTING iifname \"$DEV\" masquerade\n")
	b.WriteString("exit 0\n")
	return b.String(), nil
}

// validateTunDeviceName 与 domain.ValidateTunDevice 同契约；本包内短名防传错层。
func validateTunDeviceName(dev string) error {
	if dev == "" {
		return nil
	}
	if len(dev) > 15 {
		return fmt.Errorf("接口名 %q 超长（≤15）", dev)
	}
	for _, r := range dev {
		ok := r == '.' || r == '_' || r == '-' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return fmt.Errorf("接口名 %q 含非法字符", dev)
		}
	}
	return nil
}
