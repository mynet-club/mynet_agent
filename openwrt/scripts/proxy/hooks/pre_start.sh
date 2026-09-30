#!/bin/sh
#
# Proxy plugin — pre_start hook
# Called by rc.mynet before GNB starts.
# Ensures GNB route.conf proxy injection is present before GNB starts.
#

log_proxy() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] [proxy/pre_start] $1"; }

if [ -z "$MYNET_HOME" ] || [ ! -d "$MYNET_HOME" ]; then
    echo "[proxy/pre_start] MYNET_HOME not set or missing"
    exit 0
fi

if ! command -v lua >/dev/null 2>&1; then
    log_proxy "ERROR: lua runtime not found"
    exit 1
fi

log_proxy "ensuring proxy route injection before GNB start"
MYNET_HOME="$MYNET_HOME" lua <<'LUA' 2>&1 || {
local proxy = require("luci.model.mynet.proxy")
local ok, msg = proxy.route_inject()
if not ok then
    io.stderr:write((msg or "proxy.route_inject failed") .. "\n")
    os.exit(1)
end
if msg and msg ~= "" then
    io.stdout:write(msg .. "\n")
end
LUA
    log_proxy "ERROR: proxy.route_inject failed (exit $?)"
    exit 1
}

log_proxy "proxy pre_start completed"
exit 0
