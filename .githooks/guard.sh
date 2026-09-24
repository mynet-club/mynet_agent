#!/bin/sh
# guard.sh — 安全 / 结构 / 产物 红线守护（pre-commit 调用，秒级）
#
# 设计原则：**精确不激进**。
#   误报过多的守护会被 `--no-verify` 绕过，反而失效。宁可漏报，不可大量误报。
#   - 正则取窄，只抓确凿模式
#   - 显式豁免：把 `GUARD-OK <原因>` 写在**违规那一行**，本行即豁免
#   - 每次豁免记入 .git/guard-exempt.log，便于审计
#
# 检查范围（两种语义，**必须在输出里说清是哪一种**）：
#   - 有暂存内容 → 查暂存区。这是 pre-commit 的语义：拦住即将入库的东西。
#   - 无暂存内容 → 查工作区（未暂存修改 + 未跟踪且未被 ignore 的文件）。
#     这是 `make guard` / `make check` 的语义。
#     ⚠️ 这一支此前是 `exit 0`（打印「无暂存文件」就算通过），于是脏工作区
#     一条红线都不查 —— 九个仓的 guard 环节全线空转，包括 494→521 行的
#     logger 重写。教训同 AGENTS.md §2.6：**「没东西可查」型门禁等于没有门禁**，
#     它把「没跑」伪装成「通过」。真要跳过，也必须打印明确的跳过标记。
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

# ── 确定检查范围 ─────────────────────────────────────────────
STAGED=$(git diff --cached --name-only --diff-filter=ACMR 2>/dev/null || true)
if [ -n "$STAGED" ]; then
  MODE=staged
  FILES=$STAGED
else
  MODE=worktree
  # 工作区 vs 索引（索引此时等于 HEAD）+ 未跟踪但未被 ignore 的文件
  FILES=$( { git diff --name-only --diff-filter=ACMR 2>/dev/null || true
             git ls-files --others --exclude-standard 2>/dev/null || true
           } | sort -u )
fi

if [ -z "$FILES" ]; then
  echo "  (无可检文件：无暂存、无修改、无未跟踪 —— 本次未检查任何红线)"
  exit 0
fi

NFILES=$(printf '%s\n' "$FILES" | grep -c . || true)
if [ "$MODE" = staged ]; then
  echo "  · 范围：暂存区（${NFILES} 个文件）"
else
  echo "  · 范围：工作区（${NFILES} 个文件；索引为空，按 make guard 语义查未提交内容）"
fi

# 某个文件的新增行。未跟踪文件没有 diff 可比，用 --no-index 对 /dev/null 取全文。
added_in_file() {
  _f=$1
  if [ "$MODE" = staged ]; then
    git diff --cached -U0 -- "$_f" 2>/dev/null | grep -E '^\+' | grep -v '^+++' || true
  elif git ls-files --error-unmatch "$_f" >/dev/null 2>&1; then
    git diff -U0 -- "$_f" 2>/dev/null | grep -E '^\+' | grep -v '^+++' || true
  else
    git diff --no-index -U0 -- /dev/null "$_f" 2>/dev/null | grep -E '^\+' | grep -v '^+++' || true
  fi
}

# 全量新增行（用于 A1/A2/C3 这类不分文件的红线）
ADDED=""
for f in $FILES; do
  ADDED="$ADDED
$(added_in_file "$f")"
done

# 剔除带 GUARD-OK 的行（显式豁免），并记审计日志
EXEMPTED=$(printf '%s\n' "$ADDED" | grep 'GUARD-OK' || true)
ADDED=$(printf '%s\n' "$ADDED" | grep -v 'GUARD-OK' || true)
if [ -n "$EXEMPTED" ]; then
  mkdir -p "$(dirname "$EXEMPT_LOG")"
  printf '%s | %s\n' "$(date -u +%FT%TZ)" "$EXEMPTED" >> "$EXEMPT_LOG"
  echo "  ⚠ 已豁免 $(printf '%s\n' "$EXEMPTED" | grep -c . | tr -d ' ') 行（GUARD-OK），见 .git/guard-exempt.log"
fi

# ── A 安全红线 ───────────────────────────────────────────────
# A1 硬编码凭据：只抓「标识符 = 明文值」的确凿模式，避免撞注释/文档
# 关键字前必须是非字母，避免 DirToken / refresh_token / "var/token" 这类误报
# 双/单引号各查一次（BSD grep 不支持 \xNN 转义，故拆两条）
# A1 双引号版：引号内**以 $ 开头**的一律不算（那是变量引用或命令替换，不是字面量）。
A1D="[^[:alpha:]](password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key)[^=]{0,24}=[[:space:]]*\"[^$\"][^\"]{7,}\""
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
    .env|.env.*|*.pem|*.key|id_rsa*|id_ed25519*|*.private)
      case "$b" in
        .env.example|.env.sample|.env.template) ;; # 无密钥模板，允许
        *) bad "A3 敏感文件不得入库: $f"; a3=1 ;;
      esac ;;
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
#    详见 AGENTS.md §1.2 —— 只扫 mynetd/ 的**import 路径**（注释里提到 detector 是允许的）
B2PAT='mynet/detector|mynet/tools|mynet/apply|mynet/metrics|mynet/service|gopsutil|shirou/gopsutil' # GUARD-OK 自检正则
b2=0
for f in $FILES; do
  case "$f" in
    mynetd/*)
      if added_in_file "$f" | grep -qE "$B2PAT"; then
        bad "B2 mynetd 不得依赖系统能力（执行器架构）: $f"; b2=1
      fi ;;
  esac
done
[ "$b2" -eq 0 ] && ok "B2 mynetd 未依赖系统能力"

# B3 relaypolicy 不含鉴权逻辑（relay 不做鉴权，鉴权归 back）—— 只扫 relaypolicy/
B3PAT='Authorization|Sanctum|Rbac|CheckPermission|auth:sanctum|VerifySignature' # GUARD-OK 自检正则
b3=0
for f in $FILES; do
  case "$f" in
    *relaypolicy*)
      if added_in_file "$f" | grep -qE "$B3PAT"; then
        bad "B3 relaypolicy 只做路径白名单，不得含鉴权逻辑: $f"; b3=1
      fi ;;
  esac
done
[ "$b3" -eq 0 ] && ok "B3 relaypolicy 未含鉴权"

# B4 业务对象不含驱动术语（驱动差异不下泄到业务对象）
#    只查业务模型 Models/*.php；BundleEnvelope / VPNBundle 契约里**允许**驱动字段
#    （entities.go / vpn_peer.go 是 envelope schema，不是业务对象）
b4=0
for f in $FILES; do
  case "$f" in
    *Models/*.php)
      if added_in_file "$f" | grep -qE 'AllowedIPs|node\.conf|\bnode_conf\b|gnb_tun|\bwg0\b'; then
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

# C2 端口漂移：本机 API / WebUI 端口应为 8096（只扫源码，不扫 docs/.githooks）
C2PAT='127\.0\.0\.1:8720|127\.0\.0\.1:8008|localhost:8720|localhost:8008' # GUARD-OK 自检正则
c2=0
for f in $FILES; do
  case "$f" in
    docs/*|*/docs/*|.githooks/*|*/.githooks/*|*.md) continue ;;
  esac
  if added_in_file "$f" | grep -qE "$C2PAT"; then
    bad "C2 端口漂移（本机 API/WebUI 应为 8096）: $f"; c2=1
  fi
done
[ "$c2" -eq 0 ] && ok "C2 端口无漂移"

# C3 命名漂移：产物前缀应为 mynet_agent_*（不是 agent_*）
if printf '%s\n' "$ADDED" | grep -qE '(^|[^a-zA-Z_])agent_(linux|darwin|windows|openwrt|freebsd)'; then # GUARD-OK 自检正则（不误伤 mynet_agent_*）
  bad "C3 命名漂移（产物前缀应为 mynet_agent_*，不是 agent_*）"
else
  ok "C3 命名无漂移"
fi

# ── 汇总 ─────────────────────────────────────────────────────
[ "$fail" -eq 0 ] && echo "  ✓ guard 通过" || { echo "  ✗ guard 拦下（若确为误报，在违规那一行加 'GUARD-OK <原因>'）"; exit 1; }
