#!/bin/sh
# mynet-heartbeat — cron 包装器（主逻辑在 Lua）
#
# 调用 system.run_daemon_heartbeat()：
#   - 读取节点公钥 → HMAC-SHA256 签名 → POST /api/v2/monitor/heartbeat
#   - 认证头：X-Node-Id + X-Timestamp + X-Node-Signature（无 JWT token）
#   - 与 Go mynetd 完全对齐，永不过期
#
# cron（postinst 自动写入）：
#   */5 * * * * /usr/bin/mynet-heartbeat
#
# 依赖：lua, curl, sha256sum(busybox), base64(busybox)

MYNET_HOME="${MYNET_HOME:-/etc/mynet}"
LOG="${MYNET_HOME}/var/logs/heartbeat.log"
LOCK="/var/run/mynet_heartbeat.lock"

# 防重入
if [ -f "$LOCK" ]; then
    old_pid=$(cat "$LOCK" 2>/dev/null)
    if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
        exit 0
    fi
fi
mkdir -p "$(dirname "$LOCK")"
echo $$ > "$LOCK"
trap 'rm -f "$LOCK"' EXIT

# 日志（保留最后 500 行）
mkdir -p "$(dirname "$LOG")"
_log() {
    printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >> "$LOG"
    cnt=$(wc -l < "$LOG" 2>/dev/null || echo 0)
    [ "$cnt" -gt 500 ] && tail -200 "$LOG" > "$LOG.tmp" && mv "$LOG.tmp" "$LOG"
}

# Lua 路径（OpenWrt LuCI 标准位置）
export LUA_PATH="/usr/lib/lua/?.lua;/usr/lib/lua/?/init.lua;;"

lua -e "
    local sys = require('luci.model.mynet.system')
    local ok, err = sys.run_daemon_heartbeat()
    if ok then
        io.stdout:write('ok\n')
    else
        io.stderr:write('ERROR: ' .. tostring(err) .. '\n')
        os.exit(1)
    end
" >> "$LOG" 2>&1
rc=$?

if [ $rc -eq 0 ]; then
    _log "✓ ok"
else
    _log "✗ failed (see above)"
fi

# ── GNB peer 健康检查 + 自愈重启 ────────────────────────────────
# E-3 判据改造（2026-09-23）：
#   旧判据 = proxy_state.json 存在 + PROXY_GATEWAYS 首个 peer VIP 不可达
#            ——完全依赖已下架的 mynet_proxy，必须先改造再搬
#   新判据 = gnb_ctl -s 的 PONG 状态 / ping 对端 VPN IP
# 保留隐性知识：15 分钟冷却 + 启动 90s 宽限（持续不通时防循环抖动）
_gnb_peer_heal() {
    local heal_ts_file="/var/run/mynet_gnb_heal.ts"
    local HEAL_COOLDOWN=900   # 15 分钟内只重启一次
    local boot_ts_file="/var/run/mynet_gnb_heal.boot"

    # 启动 90s 宽限：以本脚本首次运行时间近似 GNB 启动
    local now_ts; now_ts=$(date +%s)
    if [ ! -f "$boot_ts_file" ]; then
        echo "$now_ts" > "$boot_ts_file"
        return 0
    fi
    local boot_ts; boot_ts=$(cat "$boot_ts_file" 2>/dev/null)
    if [ -n "$boot_ts" ] && [ $((now_ts - boot_ts)) -lt 90 ]; then
        _log "peer-heal: within 90s grace, skip"
        return 0
    fi

    # 冷却
    if [ -f "$heal_ts_file" ]; then
        local last_heal; last_heal=$(cat "$heal_ts_file" 2>/dev/null)
        if [ -n "$last_heal" ] && [ $((now_ts - last_heal)) -lt $HEAL_COOLDOWN ]; then
            return 0
        fi
    fi

    # 新判据：gnb_ctl -s 找无 PONG 的 peer；或对 peer VPN IP ping 失败
    local status_out peer_vip
    status_out=$(gnb_ctl -s 2>/dev/null) || return 0
    # 取第一个仍无 PONG 的 peer 的 VPN IP（格式随 gnb_ctl -s 输出；尽力解析）
    peer_vip=$(printf '%s\n' "$status_out" | awk '/PONG/{next} /10\./{print $NF; exit}')
    if [ -z "$peer_vip" ]; then
        # 全部 PONG 或无法解析 → 不 heal
        return 0
    fi

    if ping -c 3 -w 8 "$peer_vip" >/dev/null 2>&1; then
        return 0
    fi

    _log "peer-heal: $peer_vip unreachable (no PONG) — restarting GNB..."
    echo "$now_ts" > "$heal_ts_file"

    lua -e "
local node = require('luci.model.mynet.node')
local cfg  = require('luci.model.mynet.config')
local nid  = cfg.get_node_id()
if not nid then io.stderr:write('peer-heal: NODE_ID not configured\n'); os.exit(1) end
local ok, err = node.restart_gnb(nid)
if ok then
    io.stdout:write('peer-heal: GNB restarted OK\n')
else
    io.stderr:write('peer-heal: restart failed: ' .. tostring(err) .. '\n')
    os.exit(1)
end
" >> "$LOG" 2>&1
    local heal_rc=$?
    if [ $heal_rc -eq 0 ]; then
        _log "peer-heal: GNB restarted successfully"
    else
        _log "peer-heal: GNB restart failed (rc=$heal_rc)"
    fi
}

_gnb_peer_heal
