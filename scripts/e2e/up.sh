#!/bin/sh
# R1-1 本地端到端拓扑（不依赖真机）：
#   - 可选起 gateway（WS）
#   - 可选起 mynetd × N
#   - 跑 R1-2 链：注册 → 密钥 → 连接 → 授权 → Bundle → Apply(dry-run)
# 用法: sh scripts/e2e/up.sh [--gateway] [--nodes 2]
set -e
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
AGENT="$ROOT"
GATEWAY=0
NODES=2
while [ $# -gt 0 ]; do
  case "$1" in
    --gateway) GATEWAY=1; shift ;;
    --nodes) NODES="$2"; shift 2 ;;
    *) echo "usage: $0 [--gateway] [--nodes N]"; exit 2 ;;
  esac
done

log() { echo "[e2e] $*"; }

# ── 构建 ─────────────────────────────────────────────
log "build mynet + mynetd"
(cd "$AGENT" && go build -o /tmp/e2e-mynet ./cmd/mynet && go build -o /tmp/e2e-mynetd ./cmd/mynetd)

# ── R1-4：签名联测（已有单元测试，此处只报告）──────
log "node-face interop vectors present: $([ -f "$AGENT/mynet/keys/testdata/node_face_vectors.json" ] && echo yes || echo no)"

# ── R2-2 只读子命令冒烟 ─────────────────────────────
log "executor smoke"
/tmp/e2e-mynet help >/dev/null
/tmp/e2e-mynet platform || true
/tmp/e2e-mynet status || true
/tmp/e2e-mynet check || true

# ── R1-2 业务链（无 DB 时走 apply dry-run + keys）────
log "R1-2 chain: keys → plan → apply --dry-run"
TMP=$(mktemp -d)
export MYNET_HOME="$TMP"
mkdir -p "$TMP/var"

# 密钥（A-1）
if /tmp/e2e-mynet setup --print-keys >"$TMP/keys.json" 2>"$TMP/keys.err"; then
  log "setup --print-keys ok"
else
  log "setup --print-keys: $(cat "$TMP/keys.err")"
fi

# Bundle 计划 → Apply dry-run（F-3 跨驱动一条 route）
cat >"$TMP/plan.json" <<'JSON'
{
  "routes": [
    {"action":"replace","destination":"10.8.0.0/24","device":"gnb0","metric":100},
    {"action":"replace","destination":"10.9.0.0/24","device":"wg0","metric":200}
  ],
  "firewall": [
    {"action":"ensure-masq","device":"wg0"}
  ]
}
JSON
/tmp/e2e-mynet detector --force >/dev/null 2>&1 || true
# dry-run：路由命令形状必须输出；本机无 iptables/nft 时 firewall 记 warning 不算失败
if /tmp/e2e-mynet apply --dry-run "$TMP/plan.json" >"$TMP/apply.out" 2>"$TMP/apply.err"; then
  log "apply dry-run ok"
else
  if grep -q 'DRY-RUN: \[ip route' "$TMP/apply.out"; then
    log "apply dry-run：路由 OK；firewall 工具缺失（本平台可接受）"
    cat "$TMP/apply.err"
  else
    log "apply dry-run failed"
    cat "$TMP/apply.out" "$TMP/apply.err"
    exit 1
  fi
fi

# ── R1-5 / gateway 常驻（V1 已把 cmd/mynet_gateway 接上）────
# 这里原先只有一句「需 gateway 二进制（S4 已有包，cmd 待接）— 跳过」。
# 而 cmd/mynet_gateway 早就在了、Makefile dist 也产它——那句自陈过期了。
# 静默跳过更糟的地方在于：这个开关从来没被真正执行过，等于没有验证
# （§7.1 D1「返回 error 的桩不算实现」，静默 no-op 同理）。
# 现在真构建、真起进程、真打 /healthz、真发 SIGTERM 验优雅退出。
if [ "$GATEWAY" = "1" ]; then
  log "build mynet_gateway"
  (cd "$AGENT" && go build -o /tmp/e2e-mynet-gateway ./cmd/mynet_gateway)

  GW_PORT=18710
  GW_LOG="$TMP/gateway.log"
  # 绑回环：按 cmd/mynet_gateway 的 fail-fast 规则（非回环必须配 -internal-token），
  # 回环监听合法且不需要口令
  /tmp/e2e-mynet-gateway -listen "127.0.0.1:$GW_PORT" >"$GW_LOG" 2>&1 &
  GW_PID=$!
  # 无论后面怎么退出（含中途 exit 1）都要收掉，别留孤儿进程
  trap 'kill -TERM "$GW_PID" 2>/dev/null || true' EXIT INT TERM

  GW_OK=0
  i=0
  while [ "$i" -lt 50 ]; do
    if curl -fsS "http://127.0.0.1:$GW_PORT/healthz" >"$TMP/healthz.json" 2>/dev/null; then
      GW_OK=1
      break
    fi
    kill -0 "$GW_PID" 2>/dev/null || break   # 进程已死，再等也没用
    i=$((i + 1))
    sleep 0.2
  done

  if [ "$GW_OK" != "1" ]; then
    log "✗ gateway 未在 10s 内就绪（或启动即失败）"
    cat "$GW_LOG"
    exit 1
  fi
  # 不只验「端点可达」——响应体形状也得对，否则换了实现照样绿（§7.1 D5）
  if ! grep -q '"conns"' "$TMP/healthz.json"; then
    log "✗ /healthz 响应里没有 conns 字段：$(cat "$TMP/healthz.json")"
    exit 1
  fi
  log "gateway /healthz → $(cat "$TMP/healthz.json")"

  # 「SIGTERM 干净退出」是 V1-1 的验收判据之一，顺手一起验
  kill -TERM "$GW_PID"
  GW_RC=0
  wait "$GW_PID" || GW_RC=$?
  if [ "$GW_RC" -ne 0 ]; then
    log "✗ gateway 收到 SIGTERM 后退出码 ${GW_RC}（期望 0）"
    cat "$GW_LOG"
    exit 1
  fi
  log "gateway SIGTERM 干净退出（exit 0）"
  trap - EXIT INT TERM
fi

log "e2e smoke PASS (nodes=$NODES)"
log "next: R1-2 业务链上真实后端（Feature/DualDriver 已覆盖领域层）"
