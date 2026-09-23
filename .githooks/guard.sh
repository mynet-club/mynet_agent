#!/bin/sh
# guard.sh — 安全 / 结构 / 产物 红线守护（pre-commit 调用，秒级）
#
# 设计原则：**精确不激进**。
#   误报过多的守护会被 `--no-verify` 绕过，反而失效。宁可漏报，不可大量误报。
#   - 正则取窄，只抓确凿模式
#   - 显式豁免：把 `GUARD-OK <原因>` 写在**违规那一行**，本行即豁免
#   - 每次豁免记入 .git/guard-exempt.log，便于审计
#
# 规则来源：AGENTS.md §2.6 · §3 安全红线 · §1 硬约束
# ─────────────────────────────────────────────────────────────
set -e

fail=0
bad() { printf '  ✗ %s\n' "$1"; fail=1; }
ok()  { printf '  ✓ %s\n' "$1"; }

ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
cd "$ROOT"
EXEMPT_LOG="$ROOT/.git/guard-exempt.log"

FILES=$(git diff --cached --name-only --diff-filter=ACMR 2>/dev/null || true)
[ -z "$FILES" ] && { echo "  (无暂存文件)"; exit 0; }

# 新增行；剔除掉带 GUARD-OK 的行（显式豁免），并记审计日志
ADDED=$(git diff --cached -U0 2>/dev/null | grep -E '^\+' | grep -v '^+++' || true)
EXEMPTED=$(printf '%s\n' "$ADDED" | grep 'GUARD-OK' || true)
ADDED=$(printf '%s\n' "$ADDED" | grep -v 'GUARD-OK' || true)
if [ -n "$EXEMPTED" ]; then
  mkdir -p "$(dirname "$EXEMPT_LOG")"
  printf '%s | %s\n' "$(date -u +%FT%TZ)" "$EXEMPTED" >> "$EXEMPT_LOG"
  echo "  ⚠ 已豁免 $(printf '%s\n' "$EXEMPTED" | wc -l | tr -d ' ') 行（GUARD-OK），见 .git/guard-exempt.log"
fi

# 只看某个子树下的新增行（用于结构红线）
added_in() {
  printf '%s\n' "$ADDED" | grep -F "$1" || true
}

# ── A 安全红线 ───────────────────────────────────────────────
# A1 硬编码凭据：只抓「标识符 = 明文值」的确凿模式，避免撞注释/文档
# 关键字前必须是非字母，避免 DirToken / refresh_token / "var/token" 这类误报
# 双/单引号各查一次（BSD grep 不支持 \xNN 转义，故拆两条）
A1D="[^[:alpha:]](password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key)[^=]{0,24}=[[:space:]]*\"[^\"]{8,}"
A1S="[^[:alpha:]](password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key)[^=]{0,24}=[[:space:]]*'[^']{8,}"
if printf '%s\n' "$ADDED" | grep -qiE "$A1D" || printf '%s\n' "$ADDED" | grep -qiE "$A1S"; then
  bad "A1 硬编码凭据（identifier = 引号包裹的长字面量）"
  printf '%s\n' "$ADDED" | grep -iE "(password|passwd|secret|token|api[_-]?key|access[_-]?key)[^=]*=" | head -5 | sed 's/^/      /'
else
  ok "A1 无硬编码凭据"
fi

# A2 私钥
if printf '%s\n' "$ADDED" | grep -qE 'BEGIN (RSA |EC |OPENSSH |PGP )?PRIVATE KEY'; then
  bad "A2 私钥泄露（BEGIN … PRIVATE KEY）"
else
  ok "A2 无私钥泄露"
fi

# A3 敏感文件名
a3=0
for f in $FILES; do
  b=$(basename "$f")
  case "$b" in
    .env|.env.*|*.pem|*.key|id_rsa*|id_ed25519*|*.private) bad "A3 敏感文件不得入库: $f"; a3=1 ;;
  esac
done
[ "$a3" -eq 0 ] && ok "A3 无敏感文件"

# ── B 结构红线（架构约束，违反会静默扩散）──────────────────
# B1 mynet_gnb/src/ 只读 —— 补丁包机制依赖上游树原样
b1=0
case "$ROOT" in
  *mynet_gnb*)
    for f in $FILES; do
      case "$f" in
        src/*) bad "B1 mynet_gnb/src/ 是上游树，改动须走补丁包（packages/）: $f"; b1=1 ;;
      esac
    done ;;
esac
[ "$b1" -eq 0 ] && ok "B1 mynet_gnb/src/ 未被直改"

# B2 mynetd 不得依赖系统能力（执行器架构：mynetd 纯通信）
#    详见 AGENTS.md §1.2 —— 探测/写入/驱动/进程管理都在执行器
if printf '%s\n' "$ADDED" | grep -E 'mynetd/' >/dev/null 2>&1 && \
   printf '%s\n' "$ADDED" | grep -qE '"(mynet/)?(detector|tools/(firewall|routing)|infrastructure|coordinator|node/|service/)|gopsutil'; then
  bad "B2 mynetd 不得依赖系统能力（执行器架构）"
  printf '%s\n' "$ADDED" | grep -E '(detector|tools/(firewall|routing)|infrastructure|coordinator|node/|service/|gopsutil)' | head -3 | sed 's/^/      /'
else
  ok "B2 mynetd 未依赖系统能力"
fi

# B3 relaypolicy 不含鉴权逻辑（relay 不做鉴权，鉴权归 back）
if printf '%s\n' "$ADDED" | grep -F 'relaypolicy' >/dev/null 2>&1 && \
   printf '%s\n' "$ADDED" | grep -qE 'Authorization|Sanctum|Rbac|CheckPermission|auth:sanctum|VerifySignature'; then
  bad "B3 relaypolicy 只做路径白名单，不得含鉴权逻辑"
else
  ok "B3 relaypolicy 未含鉴权"
fi

# B4 业务对象不含驱动术语（驱动差异不下泄到业务对象）
#    只查业务模型 Models/*.php；BundleEnvelope / VPNBundle 契约里**允许**驱动字段
#    （entities.go / vpn_peer.go 是 envelope schema，不是业务对象）
b4=0
for f in $FILES; do
  case "$f" in
    *Models/*.php)
      if git diff --cached -U0 -- "$f" 2>/dev/null | grep -E '^\+' | grep -v '^+++' | grep -qE 'AllowedIPs|node\.conf|node_conf|gnb_tun|\bwg0\b'; then
        bad "B4 业务对象不得含驱动术语（AllowedIPs/node.conf/gnb_tun/wg0）: $f"; b4=1
      fi ;;
  esac
done
[ "$b4" -eq 0 ] && ok "B4 业务对象无驱动术语"

# ── C 产物 / 一致性红线 ─────────────────────────────────────
# C1 格式化 bug 产物文件名（Go %!d(string=…) 那类）
c1=0
for f in $FILES; do
  case "$f" in
    *'%!'*|*'string='*) bad "C1 格式化 bug 产物文件名: $f"; c1=1 ;;
  esac
done
[ "$c1" -eq 0 ] && ok "C1 无格式化产物文件名"

# C2 端口漂移：本机 API / WebUI 端口应为 8096
if printf '%s\n' "$ADDED" | grep -qE '127\.0\.0\.1:8720|127\.0\.0\.1:8008|localhost:8720|localhost:8008'; then
  bad "C2 端口漂移（本机 API/WebUI 应为 8096）"
  printf '%s\n' "$ADDED" | grep -E '8720|127\.0\.0\.1:8008' | head -3 | sed 's/^/      /'
else
  ok "C2 端口无漂移"
fi

# C3 命名漂移：产物前缀应为 mynet_agent_*（不是 agent_*）
if printf '%s\n' "$ADDED" | grep -qE 'agent_(linux|darwin|windows|openwrt|freebsd)'; then
  bad "C3 命名漂移（产物前缀应为 mynet_agent_*，不是 agent_*）"
else
  ok "C3 命名无漂移"
fi

# ── 汇总 ─────────────────────────────────────────────────────
[ "$fail" -eq 0 ] && echo "  ✓ guard 通过" || { echo "  ✗ guard 拦下（若确为误报，在违规那一行加 'GUARD-OK <原因>'）"; exit 1; }
