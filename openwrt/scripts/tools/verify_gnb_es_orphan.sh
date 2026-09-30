#!/bin/sh
# E-1 前置验证：gnb_es 子进程在 gnb 崩溃后是否会留孤儿（PPID=1）。
#
# 背景（隐性知识）：Lua 靠 PPID 匹配 + 按二进制路径兜底清理 gnb_es。
# procd respawn 接管 gnb 前必须验证——respawn 只拉 gnb，不清理 gnb_es。
#
# 用法（实机 root）：
#   sh verify_gnb_es_orphan.sh           # 现状报告
#   GNB_PID=1234 sh verify_gnb_es_orphan.sh --crash
#   GNB_CONF=/etc/mynet/driver/gnb/conf/<id> sh verify_gnb_es_orphan.sh --crash
#   sh verify_gnb_es_orphan.sh --clean   # 清理孤儿 gnb_es（仅本安装路径）
#
# 退出码：0=无孤儿/清理完成；1=发现孤儿且未 --clean；2=环境不满足
set -e

GNB_BIN_DIR="${GNB_BIN_DIR:-/usr/bin}"
MODE="${1:-report}"

log() { echo "[verify-gnb-es] $*"; }

count_gnb_es() {
    pidof gnb_es 2>/dev/null | wc -w | tr -d ' '
}

list_gnb_es() {
    pidof gnb_es 2>/dev/null | tr ' ' '\n' | while read -r pid; do
        [ -n "$pid" ] || continue
        ppid=$(awk '{print $4}' "/proc/$pid/stat" 2>/dev/null || echo '?')
        exe=$(readlink "/proc/$pid/exe" 2>/dev/null || echo '?')
        echo "pid=$pid ppid=$ppid exe=$exe"
    done
}

list_orphans() {
    # PPID=1 且二进制在本安装路径 → 孤儿
    pidof gnb_es 2>/dev/null | tr ' ' '\n' | while read -r pid; do
        [ -n "$pid" ] || continue
        ppid=$(awk '{print $4}' "/proc/$pid/stat" 2>/dev/null || echo '')
        exe=$(readlink "/proc/$pid/exe" 2>/dev/null || echo '')
        if [ "$ppid" = "1" ]; then
            case "$exe" in
                "$GNB_BIN_DIR"/*) echo "$pid" ;;
            esac
        fi
    done
}

case "$MODE" in
report)
    log "gnb_es count=$(count_gnb_es)"
    list_gnb_es
    orphans=$(list_orphans | tr '\n' ' ')
    if [ -n "$orphans" ]; then
        log "ORPHANS: $orphans"
        exit 1
    fi
    log "no orphans"
    ;;
--crash)
    # 模拟崩溃：杀 **指定实例** 的 gnb，观察 gnb_es 是否被回收
    # 多实例安全：优先 GNB_PID / GNB_CONF cmdline 匹配，绝不 pidof 取第一个
    gpid="${GNB_PID:-}"
    if [ -z "$gpid" ] && [ -n "$GNB_CONF" ]; then
        gpid=$(pgrep -f "gnb -c $GNB_CONF" 2>/dev/null | head -1)
    fi
    if [ -z "$gpid" ]; then
        # 铁律：禁止按名字杀。必须显式 GNB_PID 或 GNB_CONF
        log "refuse: set GNB_PID=<pid> or GNB_CONF=<confdir> (never kill by name)"
        exit 2
    fi
    before=$(count_gnb_es)
    log "gnb pid=$gpid, gnb_es=$before; SIGKILL gnb..."
    kill -9 "$gpid" 2>/dev/null || true
    i=0
    while [ $i -lt 6 ]; do
        sleep 1
        i=$((i + 1))
        now=$(count_gnb_es)
        log "t+${i}s gnb_es=$now"
    done
    orphans=$(list_orphans | tr '\n' ' ')
    if [ -n "$orphans" ]; then
        log "FAIL: leftover orphans: $orphans"
        log "procd respawn 前必须先解决（跑 --clean 或修 Lua 清理）"
        exit 1
    fi
    log "PASS: no gnb_es orphans after gnb crash"
    ;;
--clean)
    for pid in $(list_orphans); do
        log "killing orphan gnb_es pid=$pid"
        kill "$pid" 2>/dev/null || kill -9 "$pid" 2>/dev/null || true
    done
    log "clean done; remaining=$(count_gnb_es)"
    ;;
--inspect)
    # 定位失明时的人工判断台：只列候选，不杀
    # 何时失明：conf 目录改名/迁移、符号链接启动、sudo 壳进程、PID 复用
    # 注意：改端口**不会**失明——cmdline 只有 -c <conf_dir>，端口在 node.conf 里
    echo "=== gnb / gnb_es 候选（人工判断，不自动杀）==="
    for pid in $(pidof gnb 2>/dev/null) $(pidof gnb_es 2>/dev/null); do
        [ -d "/proc/$pid" ] || continue
        cmd=$(tr '\0' ' ' < /proc/$pid/cmdline 2>/dev/null)
        exe=$(readlink /proc/$pid/exe 2>/dev/null)
        ppid=$(awk '{print $4}' /proc/$pid/stat 2>/dev/null)
        echo "pid=$pid ppid=$ppid exe=$exe"
        echo "  cmd=$cmd"
    done
    echo "--- 端口占用 ---"
    netstat -uln 2>/dev/null | grep -E ':(90[0-9][0-9]|92[0-9][0-9])' || true
    echo "--- TUN ---"
    ip -o link show 2>/dev/null | grep -E 'gnb|tun' || true
    ;;
*)
    echo "usage: $0 [report|--inspect|--crash|--clean]"
    echo "  --inspect  列候选供人工判断（conf 改名/符号链接/sudo 壳/PID 复用时用）"
    echo "  --crash    需 GNB_PID 或 GNB_CONF"
    exit 2
    ;;
esac
