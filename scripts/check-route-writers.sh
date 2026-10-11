#!/bin/sh
# check-route-writers.sh — F-5 / N3：OS 路由与防火墙唯一写入点审计
#
# 期望：
#   `ip route (add|replace|del|delete)` / `route -n (add|delete)` / `netsh … route`
#   `nft (add|insert|delete|flush)` / `iptables ` / `ip6tables `
# 的**执行点**只在：
#   mynet_agent/mynet/apply_plan.go        —— OS 路由 + 防火墙（L6 唯一写入者）
#   mynet_agent/mynet/proxy_apply.go       —— 旧 L6 proxy 策略路由（GUARD-OK）
#   mynet_agent/mynet/proxy_exit_firewall.go —— 出口 ACL/NAT，`nft -f` 原子替换；
#                                              由 apply_plan.go:368 调用，是**唯一写入者的
#                                              辅助**，非独立写入者（GUARD-OK）
#   mynet_agent/mynet/proxy_iplist.go      —— 代理 IP 集合片段 `nft -f <片段>`（旧 L6，GUARD-OK）
#   mynet_agent/mynet/proxy_dns*.go        —— DNS 分流（旧 L6，GUARD-OK）
#   **/scripts/tools/*.sh                  —— OpenWrt 侧运行时/诊断（GUARD-OK）
#   mynet_openwrt/scripts/.../route_policy.sh / proxy.lua / firewall*
#                                          —— OpenWrt 侧运行时（GUARD-OK，产品分界）
#   mynet_agent/scripts/e2e/               —— 测试
#
# 例外必须在违规行写 `GUARD-OK <原因>`（AGENTS.md §2.6）。
#
# 用法：sh scripts/check-route-writers.sh   # 0=干净，1=发现未豁免写入者

set -u
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

tmp=$(mktemp)

# 路由写入
grep -RnE 'ip route (add|replace|del|delete)|route -n (add|delete)|netsh .* route' \
    --include='*.go' --include='*.sh' --include='*.lua' \
    "$ROOT/mynet_agent" "$ROOT/mynet_openwrt" 2>/dev/null \
    | grep -v GUARD-OK \
    | grep -v check-route-writers \
    | grep -vE ':[0-9]+:[[:space:]]*(//|#|--)' \
    | grep -v 'mynet_agent/mynet/apply_plan.go:' \
    | grep -v 'mynet_agent/mynet/proxy_apply.go:' \
    | grep -v 'mynet_agent/scripts/e2e/' \
    | grep -v 'route_policy.sh:' \
    | grep -v 'proxy.lua:' \
    | grep -v 'scripts/tools/' \
    | grep -v 'firewall' \
    > "$tmp" || true

# 防火墙写入（nft / iptables）—— 同一所有权闸
#
# 两种写法都要收：① shell 形态 `nft add …`（.sh/.lua）；② Go 的 argv 形态
# `[]string{"nft", "-f", "-"}` / `{"iptables","-t","nat","-A",…}` —— 后者此前**整个看不见**
# （旧正则只匹配 `nft <verb>`，而 argv 里是 `"nft", "-f"`），`proxy_iplist.go` 的
# `nft -f` 装集合就此漏网。写标志收窄到 `-A/-D/-I/-R/-F/-N/-X/-Z`（读用 `-L`，不误伤）。
grep -RnE '(^|[^a-zA-Z_])(nft (add|insert|delete|flush|-f)|iptables |ip6tables )|"nft", *"(-f|add|insert|delete|flush)"|"i(p6)?tables".*"-[ADIRFNXZ]"' \
    --include='*.go' --include='*.sh' --include='*.lua' \
    "$ROOT/mynet_agent" "$ROOT/mynet_openwrt" 2>/dev/null \
    | grep -v GUARD-OK \
    | grep -v check-route-writers \
    | grep -vE ':[0-9]+:[[:space:]]*(//|#|--)' \
    | grep -v 'mynet_agent/mynet/apply_plan.go:' \
    | grep -v 'mynet_agent/mynet/proxy_apply.go:' \
    | grep -v 'mynet_agent/mynet/proxy_exit_firewall.go:' \
    | grep -v 'mynet_agent/mynet/proxy_iplist.go:' \
    | grep -v 'mynet_agent/mynet/proxy_dns' \
    | grep -v 'mynet_agent/scripts/e2e/' \
    | grep -v 'route_policy.sh:' \
    | grep -v 'proxy.lua:' \
    | grep -v 'scripts/tools/' \
    | grep -v 'firewall' \
    | grep -v '_test.go:' \
    >> "$tmp" || true

if [ -s "$tmp" ]; then
    echo "✗ 未豁免的路由/防火墙写入者（F-5/N3）："
    cat "$tmp"
    rm -f "$tmp"
    exit 1
fi
rm -f "$tmp"
echo "✓ F-5/N3 路由与防火墙写入点干净（仅 apply_plan.go + GUARD-OK 例外）"
exit 0
