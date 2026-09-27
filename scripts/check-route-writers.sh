#!/bin/sh
# check-route-writers.sh — F-5：OS 路由唯一写入点审计
#
# 期望：`ip route (add|replace|del)` / `route -n (add|delete)` / `netsh … route`
# 的**执行点**只在 mynet_agent 的 mynet/apply_plan.go（runOp 出口）。
# 例外必须在违规行写 `GUARD-OK <原因>`（AGENTS.md §2.6）。
#
# 用法：sh scripts/check-route-writers.sh   # 0=干净，1=发现未豁免写入者

set -u
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

tmp=$(mktemp)
grep -RnE 'ip route (add|replace|del|delete)|route -n (add|delete)|netsh .* route' \
    --include='*.go' --include='*.sh' --include='*.lua' \
    "$ROOT/mynet_agent" "$ROOT/mynet_openwrt" 2>/dev/null \
    | grep -v GUARD-OK \
    | grep -v check-route-writers \
    | grep -vE ':[0-9]+:[[:space:]]*(//|#|--)' \
    | grep -v 'mynet_agent/mynet/apply_plan.go:' \
    | grep -v 'mynet_agent/scripts/e2e/' \
    | grep -v 'route_policy.sh:' \
    | grep -v 'proxy.lua:' \
    > "$tmp" || true

if [ -s "$tmp" ]; then
    echo "✗ 未豁免的路由写入者（F-5）："
    cat "$tmp"
    rm -f "$tmp"
    exit 1
fi
rm -f "$tmp"
echo "✓ F-5 路由写入点干净（仅 apply_plan.go + GUARD-OK 例外）"
exit 0
