#!/bin/sh
#
# Proxy plugin — pre_start hook
# Called by rc.mynet before GNB starts.
#
# route.conf 由 Go agent 全量渲染，proxy 出口策略在 gnb proxy.conf（P2 补丁）。
# 本 hook 只做两件事，且都不阻断 GNB 启动：
#   1) 清理历史 Lua 注入到 route.conf 的 proxy 策略段（存量节点一次性收敛）
#   2) 校验 proxy.conf 出口策略在场，缺失则高声警告（不写半截配置）
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

log_proxy "cleansing legacy route.conf injection, checking gnb exit policy"
MYNET_HOME="$MYNET_HOME" lua <<'LUA' 2>&1 || log_proxy "WARN: pre_start lua block failed"
local ok, proxy = pcall(require, "luci.model.mynet.proxy")
if not ok or not proxy then
    io.stdout:write("proxy module unavailable: " .. tostring(proxy) .. "\n")
    os.exit(0)
end

local cleaned, msg = proxy.route_restore()
if not cleaned then
    io.stdout:write("route.conf legacy cleanup skipped: " .. tostring(msg) .. "\n")
elseif msg and msg ~= "nothing to restore" then
    io.stdout:write("route.conf: " .. msg .. "\n")
end

local ep = proxy.exit_policy_status()
if not proxy.exit_policy_ok(ep) then
    io.stdout:write(string.format(
        "WARN: gnb exit policy inactive — present=%s role=%s proxy_on=%s exits=%d path=%s"
        .. "（代理分流需带 P2 补丁的 gnb + proxy.conf，见 mynet plugin apply）",
        tostring(ep.present), tostring(ep.role), tostring(ep.proxy_on),
        #ep.exits, tostring(ep.path)) .. "\n")
end
LUA

log_proxy "proxy pre_start completed"
exit 0
