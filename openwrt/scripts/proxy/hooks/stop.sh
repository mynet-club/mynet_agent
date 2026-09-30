#!/bin/sh
#
# Proxy plugin — stop hook
# Called by rc.mynet when GNB stops.
# Only tears down nftables/ip rules (proxy.stop).
#
# NOTE: route.conf marker injection is NOT cleaned here.
#       Cleaning markers is exclusively the job of proxy.disable().
#       This ensures GNB restarts preserve proxy route injection.
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
