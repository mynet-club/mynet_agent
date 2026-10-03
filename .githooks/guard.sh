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
# 用 --git-dir 而不是 "$ROOT/.git"：worktree 的 .git 是个**文件**，mkdir -p 会直接失败，
# 于是含 GUARD-OK 的合法提交被守护自己撞死（--no-verify 绕过恰恰是该被避免的后果）。
# 上面已 cd 到 ROOT，相对路径（普通仓返回 .git）同样可用 ⇒ 普通仓行为不变。
EXEMPT_LOG="$(git rev-parse --git-dir)/guard-exempt.log"

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
# 关键字表含裸 `pass`：mynet_ctl/scripts/deploy/db-setup.sh 曾有
# `LOCAL_DB_PASS="…"` 明文口令，而 password|passwd|pwd 三个都匹配不到它 —— 真漏报。
# 加进来实测零误报（pass=0 / PASS_COUNT=3 / BYPASS="enabled" / COMPASS="north" 均不命中，
# 因为要求关键字前是**非字母**、且值必须是够长的引号字面量）。
A1D="[^[:alpha:]](password|passwd|pwd|pass|secret|token|api[_-]?key|access[_-]?key)[^=]{0,24}=[[:space:]]*\"[^$\"][^\"]{7,}\""
A1S="[^[:alpha:]](password|passwd|pwd|pass|secret|token|api[_-]?key|access[_-]?key)[^=]{0,24}=[[:space:]]*'[^']{8,}"
if printf '%s\n' "$ADDED" | grep -qiE "$A1D" || printf '%s\n' "$ADDED" | grep -qiE "$A1S"; then
  bad "A1 硬编码凭据（identifier = 引号包裹的长字面量）"
  # ⚠️ 诊断必须用**与判定相同的严格正则**。此前这里用的是一条松得多的
  # `(password|…)[^=]*=`，于是打印出来的是 guard.sh 自己的正则定义行
  # （它们含 "password…=" 字样），真正违规的那一行反被淹掉 —— 指错方向的
  # 诊断比没有诊断更糟，它会把人送去查守护本身。
  printf '%s\n' "$ADDED" | grep -iE "$A1D|$A1S" | head -5 | sed 's/^/      /'
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

# A4 归档内容扫描 —— A2/A3 都看不见压缩包**里面**
# 2026-10-04 真实事故：mynet_gnb 的 packages/*/itest/backup-*/ 把生产节点
# (10.182.236.180) 与公司整网网关 (192.168.0.2) 的 *.private、credential.json
# 打进 .tgz 提交并推到 GitHub。三层全瞎：
#   A2 只匹配 PEM 文本 `BEGIN … PRIVATE KEY`，而 GNB 的 .private 是裸 32 字节
#      ed25519 二进制（AGENTS.md §1.1：clamp(SHA512(seed))），**没有 PEM 头**；
#   A3 只匹配**入库文件名**，而入库的文件名是 `_etc_opengnb.tgz`；
#   .gitignore 只挡 dist/，泄露路径是 itest/backup-*/。
# 修法：把 A3 的敏感名规则套到归档的**内容清单**上。
# ⚠️ A4 **不支持 GUARD-OK 行内豁免** —— 二进制没有「违规那一行」可标注。
#    被 A4 拦下的归档本就不该入库，正确处置是加进 .gitignore。
#    超大归档确需放行时改 A4_MAX_BYTES；**不许静默跳过**（同开头「没东西可查
#    型门禁等于没有门禁」那条教训：跳过必须响）。
A4_MAX_BYTES=67108864 # 64MB。guard 必须秒级；实测全 workspace 仅 gnb 两个 0.68MB 的 author zip

# 归档内条目是否敏感。判据与 A3 同一套，另加 credential*.json（事故里真有）
a4_sensitive() {
  _p=$1
  case "$_p" in
    */) return 1 ;; # 目录条目
  esac
  _b=${_p##*/}
  [ -n "$_b" ] || return 1
  case "$_b" in
    .env.example | .env.sample | .env.template) return 1 ;; # 无密钥模板，与 A3 一致
  esac
  case "$_b" in
    .env | .env.* | *.pem | *.key | *.pkey | *.p12 | *.pfx | *.private | id_rsa* | id_ed25519* | credential*.json) return 0 ;;
  esac
  return 1
}

# 列出归档内容清单。取字节要分模式：暂存区的内容在索引里，工作区的在盘上。
a4_list() {
  _f=$1
  _t=$(mktemp) || return 1
  if [ "$MODE" = staged ]; then
    git show ":$_f" >"$_t" 2>/dev/null || { rm -f "$_t"; return 1; }
  else
    cat "$_f" >"$_t" 2>/dev/null || { rm -f "$_t"; return 1; }
  fi
  case "$_f" in
    *.zip) _o=$(unzip -Z1 "$_t" 2>/dev/null) || { rm -f "$_t"; return 1; } ;;
    # bsdtar / GNU tar 都会自动识别 gz/bz2/xz/zst，不需要按后缀分派解压器
    *) _o=$(tar tf "$_t" 2>/dev/null) || { rm -f "$_t"; return 1; } ;;
  esac
  rm -f "$_t"
  printf '%s\n' "$_o"
}

a4=0
a4_n=0
for f in $FILES; do
  case "$f" in
    *.tgz | *.tar | *.tar.* | *.zip) ;;
    *) continue ;;
  esac
  a4_n=$((a4_n + 1))
  if [ "$MODE" = staged ]; then
    a4_sz=$(git cat-file -s ":$f" 2>/dev/null || true)
  else
    a4_sz=$(wc -c <"$f" 2>/dev/null | tr -d '[:space:]' || true)
  fi
  # 取不到大小按 0 处理 → 继续走 a4_list，让它自己失败并报出来（fail-closed）
  case "$a4_sz" in '' | *[!0-9]*) a4_sz=0 ;; esac
  if [ "$a4_sz" -gt "$A4_MAX_BYTES" ]; then
    bad "A4 归档 ${a4_sz} 字节超上限 ${A4_MAX_BYTES}，未能扫描内容（跳过不算通过）: $f"
    a4=1
    continue
  fi
  if ! a4_entries=$(a4_list "$f"); then
    bad "A4 列不出归档内容，未能扫描（跳过不算通过）: $f"
    a4=1
    continue
  fi
  while IFS= read -r a4_e; do
    [ -n "$a4_e" ] || continue
    if a4_sensitive "$a4_e"; then
      bad "A4 归档内含敏感文件，不得入库: $f → $a4_e"
      a4=1
    fi
  done <<A4EOF
$a4_entries
A4EOF
done
if [ "$a4" -eq 0 ]; then
  if [ "$a4_n" -eq 0 ]; then
    ok "A4 本批无归档文件（未触发内容扫描）"
  else
    ok "A4 已扫描 $a4_n 个归档的内容清单，无敏感文件"
  fi
fi

# ── B 结构红线（架构约束，违反会静默扩散）──────────────────
# B1 mynet_gnb/src/ 只读 —— 补丁包机制依赖上游树原样
#    只在 mynet_gnb 仓内有意义；其他仓不能证明兄弟仓状态。
b1=0
case "$ROOT" in
  *mynet_gnb*)
    for f in $FILES; do
      case "$f" in
        src/*) bad "B1 mynet_gnb/src/ 是上游树，改动须走补丁包（packages/）: $f"; b1=1 ;;
      esac
    done
    [ "$b1" -eq 0 ] && ok "B1 mynet_gnb/src/ 未被直改"
    ;;
  *)
    echo "  · B1 跳过（当前仓非 mynet_gnb；src/ 状态请在 mynet_gnb 根目录跑 guard）"
    ;;
esac

# B2 mynetd 不得依赖系统能力或配置面（执行器架构：mynetd 纯通信）
#    详见 AGENTS.md §1.2 —— 只扫 mynetd/ 的 **import 字符串**（注释里提到 detector 是允许的）
#    系统能力 + 配置文件格式都在执行器；localapi 只做 HTTP 壳走 Ops。
#    要求 import 引号，避免误伤注释里的包名。
B2PAT='"[^"]*mynet/(detector|tools|apply|metrics|service|keys)|"[^"]*shared/config|gopsutil|shirou/gopsutil'
b2=0
for f in $FILES; do
  case "$f" in
    mynetd/*)
      if added_in_file "$f" | grep -qE "$B2PAT"; then
        bad "B2 mynetd 不得依赖系统能力/配置面（执行器架构）: $f"; b2=1
      fi ;;
  esac
done
[ "$b2" -eq 0 ] && ok "B2 mynetd 未依赖系统能力/配置面"

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
