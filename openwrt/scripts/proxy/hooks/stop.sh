#!/bin/sh
#
# Proxy plugin — stop hook
# Called by rc.mynet when GNB stops.
# Only tears down nftables/ip rules (proxy.stop).
#
# NOTE: route.conf 的写入方是 Go agent（全量渲染）。本 hook 经 proxy.stop()
#       只做一件事：幂等剔除历史 Lua 注入的 proxy 策略段，不再追加任何内容。
#

log_proxy() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] [proxy/stop] $1"; }

if [ -z "$MYNET_HOME" ] || [ ! -d "$MYNET_HOME" ]; then
    exit 0
fi

# Stop proxy (policy routing only) — unconditional
if command -v lua >/dev/null 2>&1; then
    log_proxy "stopping proxy (nftables/ip rules)..."
    MYNET_HOME="$MYNET_HOME" lua <<'LUA' 2>&1 || true
local ok, proxy = pcall(require, "luci.model.mynet.proxy")
if ok and proxy and proxy.stop then
    proxy.stop()
end
LUA
fi

# Clear proxy runtime state
rm -f "$MYNET_HOME/var/proxy_state.json" 2>/dev/null || true

log_proxy "proxy stopped"
exit 0
