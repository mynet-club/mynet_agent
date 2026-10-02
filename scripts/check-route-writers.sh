#!/bin/sh
# check-route-writers.sh — F-5 / N3：OS 路由与防火墙唯一写入点审计
#
# 期望：
#   `ip route (add|replace|del|delete)` / `route -n (add|delete)` / `netsh … route`
#   `nft (add|insert|delete|flush)` / `iptables ` / `ip6tables `
# 的**执行点**只在：
#   mynet_agent/mynet/apply_plan.go        —— OS 路由 + 防火墙（L6 唯一写入者）
#   mynet_agent/mynet/proxy_apply.go       —— 旧 L6 proxy 策略路由（GUARD-OK）
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
grep -RnE '(^|[^a-zA-Z_])(nft (add|insert|delete|flush)|iptables |ip6tables )' \
    --include='*.go' --include='*.sh' --include='*.lua' \
    "$ROOT/mynet_agent" "$ROOT/mynet_openwrt" 2>/dev/null \
    | grep -v GUARD-OK \
    | grep -v check-route-writers \
    | grep -vE ':[0-9]+:[[:space:]]*(//|#|--)' \
    | grep -v 'mynet_agent/mynet/apply_plan.go:' \
    | grep -v 'mynet_agent/mynet/proxy_apply.go:' \
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
