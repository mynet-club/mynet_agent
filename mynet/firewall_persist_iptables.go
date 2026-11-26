package mynet

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// firewallIncludeScriptIPTables 渲染 fw3/iptables 形态的 include 重放脚本。
//
// 与 nft 版（firewallIncludeScript）共用同一份上层合同 FirewallReplayRules，
// 区别只在落地方言与加载器：nft 用 `nft -f`，fw3 用 iptables 直接建自有链。
//
// 规则映射（与 nft 产物逐条等价）：
//
//	Dir=Out     oifname $DEV masquerade      → nat 链路内 `-o "$DEV" -j MASQUERADE`
//	Dir=In      iifname $DEV masquerade      → mangle FORWARD `-i "$DEV" -j MARK` +
//	                                           nat 链路内 `-m mark --mark $MARK -j MASQUERADE`
//	Dir=Forward iif $DEV oif $WAN masquerade → mangle 同上；nat 内改为
//	                                           `-m mark --mark $MARK -o "$WAN" ... comment mynet-gateway-forward`
//
// 设计要点：
//   - 用两条自有链（mangle/nat 各一条）承载规则；脚本每次先按 `-S` 的真实规则
//     逐条删除跳转（只按 `-j 链` 做 -D 匹配会因接口匹配项不齐而删不掉，2026-11-26 .184
//     实证），再清链/删链/重建/单条插回 —— 天然幂等；
//   - tunDev 为空或接口不在时走清理分支，不留悬挂规则；
//   - 不依赖 nft；加载器缺失（iptables 不存在）时明确报错而非静默 exit 0。
func firewallIncludeScriptIPTables(tunDev, wanDev string) (string, error) {
	if _, err := FirewallReplayRules(tunDev, wanDev); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(tunDev))
	mark := fmt.Sprintf("0x4e%02x%02x00/0xffffff00", sum[0], sum[1])
	tag := hex.EncodeToString(sum[:3])
	chainM := "MYNET_M_" + tag
	chainN := "MYNET_N_" + tag

	header := `#!/bin/sh
# mynet firewall include (fw3/iptables) —— 由 mynet 生成，勿手改。
set -e
LC_ALL=C
export LC_ALL
DEV=` + fmt.Sprintf("%q", tunDev) + `
WAN=` + fmt.Sprintf("%q", wanDev) + `
MARK=` + mark + `
CHAIN_M=` + chainM + `
CHAIN_N=` + chainN + `
command -v iptables >/dev/null 2>&1 || { echo "mynet: iptables not found (fw3 backend)" >&2; exit 1; }

# 按 -S 的真实规则删跳转：只写 -D ... -j CHAIN 会因接口匹配项不齐而删不掉。
del_jumps() {
	iptables -t "$1" -S "$2" 2>/dev/null | grep -F -- "-j $3" | sed 's/^-A /-D /' | while IFS= read -r r; do
		iptables -t "$1" $r 2>/dev/null || true
	done
}
drop_own() {
	del_jumps mangle FORWARD "$CHAIN_M"
	iptables -t mangle -F "$CHAIN_M" 2>/dev/null || true
	iptables -t mangle -X "$CHAIN_M" 2>/dev/null || true
	del_jumps nat POSTROUTING "$CHAIN_N"
	iptables -t nat -F "$CHAIN_N" 2>/dev/null || true
	iptables -t nat -X "$CHAIN_N" 2>/dev/null || true
}
`

	if tunDev == "" {
		// 骨架态：只清理、不重建（等 sync 按实际接口名重写）。
		return header + `
drop_own
exit 0
`, nil
	}

	var b strings.Builder
	b.WriteString(header)
	b.WriteString(`
if ! ip link show "$DEV" >/dev/null 2>&1; then
	drop_own
	logger -t firewall.mynet.include "device not ready, mynet rules cleared"
	exit 0
fi

drop_own

iptables -t mangle -N "$CHAIN_M"
iptables -t mangle -A "$CHAIN_M" -i "$DEV" -j MARK --set-xmark "$MARK"
iptables -t mangle -I FORWARD 1 -i "$DEV" -j "$CHAIN_M"

iptables -t nat -N "$CHAIN_N"
iptables -t nat -A "$CHAIN_N" -o "$DEV" -j MASQUERADE
`)
	if wanDev != "" && wanDev != tunDev {
		b.WriteString(`iptables -t nat -A "$CHAIN_N" -m mark --mark "$MARK" -o "$WAN" -j MASQUERADE -m comment --comment "`)
		b.WriteString(GatewayForwardComment)
		b.WriteString(`"
`)
	} else {
		b.WriteString(`iptables -t nat -A "$CHAIN_N" -m mark --mark "$MARK" -j MASQUERADE
`)
	}
	b.WriteString(`iptables -t nat -I POSTROUTING 1 -j "$CHAIN_N"
logger -t firewall.mynet.include "mynet iptables rules applied for $DEV"
`)
	return b.String(), nil
}
