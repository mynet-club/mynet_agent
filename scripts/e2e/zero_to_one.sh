#!/bin/sh
# zero_to_one.sh — 0→1 验收：在**真实环境**上走完「注册 → 节点 → 密钥 → 好友 →
#                          授权 → bundle → 节点面 → 真互通 → 心跳有数据」
#
# 为什么不用现成的 demo_t2.sh / t23_social.sh：
#   · demo_t2.sh 每一步都 `|| true`，最后无条件打印 DEMO_T2 DONE —— 失败被吞掉，
#     正是 AGENTS.md §7.1 D3/D4 要防的「把没通过报成通过」
#   · t23_social.sh 全程 `head -c` 打印、**一条断言都没有**（curl 不加 -f，
#     4xx/5xx 一样退出码 0），且靠硬编码本机路径 bootstrap Laravel 去绑租户 ——
#     打远端 staging 时那条路根本不存在
#   · 两者都停在「连接请求已发出」，不验授权、不验 bundle、不验互通
#
# 本脚本的规矩：每一步都断言**HTTP 状态码 + 响应里的具体字段**；
# 任一步失败立即停并说清卡在哪，不继续往下跑出一串无意义的错。
#
# 用法：
#   STAGE_API=https://stage-api.mynet.club STAGE_GW=https://stage-gw.mynet.club \
#     sh scripts/e2e/zero_to_one.sh
#   加 --preflight-only 只跑 Phase 0（部署自检，不需要节点）
#
# 依赖：curl、python3（解析 JSON；不假设装了 jq）
set -u

API="${STAGE_API:-${API:-}}"
GW="${STAGE_GW:-${GW:-}}"
TENANT="${STAGE_TENANT_ID:-1000000000000001}"
ONLY_PREFLIGHT=0
[ "${1:-}" = "--preflight-only" ] && ONLY_PREFLIGHT=1

pass=0; fail=0
ok()   { printf '  ✓ %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  ✗ %s\n' "$1"; fail=$((fail+1)); }
phase(){ printf '\n━━━ %s ━━━\n' "$1"; }
# 硬停：0→1 是一条链，前一步没成后面全是噪音
die()  { printf '\n✗ 停在：%s\n' "$1"; printf '  已通过 %s 步 / 失败 %s 步\n' "$pass" "$fail"; exit 1; }

[ -n "$API" ] || die "需要 STAGE_API（如 https://stage-api.mynet.club）"
command -v curl >/dev/null 2>&1 || die "缺 curl"
command -v python3 >/dev/null 2>&1 || die "缺 python3（用来解析 JSON，不假设装了 jq）"

# ── HTTP 助手 ────────────────────────────────────────────────
CODE=""; BODY=""
# req METHOD URL [token] [json-body] [extra-header]
req() {
  _m=$1; _u=$2; _tok=${3:-}; _body=${4:-}; _hdr=${5:-}
  # 曾经这里带 `-4`：我们的 deploy/nginx.conf 缺 `listen [::]:443`（CPI-49），
  # 走 IPv6 时请求会被别的 server block 兜走。
  # ✅ 2026-09-25 已修并复验：IPv6 解析到 2406:da18:…（AWS 新加坡），
  #    打真 API 端点 POST /api/v1/auth/login 返回 200 且与 IPv4 内容一致，
  #    所以这个绕过按当初写下的承诺去掉了（「修好 nginx 后应去掉这个 -4 再验一遍」）。
  _args="-sS -m 30 -o /tmp/z2o_body -w %{http_code} -X $_m"
  # shellcheck disable=SC2086
  set -- $_args "$_u"
  [ -n "$_tok" ]  && set -- "$@" -H "Authorization: Bearer $_tok"
  [ -n "$_body" ] && set -- "$@" -H 'Content-Type: application/json' -d "$_body"
  [ -n "$_hdr" ]  && set -- "$@" -H "$_hdr"
  set -- "$@" -H 'Accept: application/json'
  CODE=$(curl "$@" 2>/tmp/z2o_err || echo "000")
  BODY=$(cat /tmp/z2o_body 2>/dev/null || true)
  [ "$CODE" = 000 ] && BODY=$(cat /tmp/z2o_err 2>/dev/null || true)
}

# jsonget <点分路径> —— 从 BODY 里取值；取不到返回空
jsonget() {
  printf '%s' "$BODY" | python3 -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(0)
for k in sys.argv[1].split("."):
    if isinstance(d,list):
        try: d=d[int(k)]
        except Exception: sys.exit(0)
    elif isinstance(d,dict) and k in d: d=d[k]
    else: sys.exit(0)
print(d if not isinstance(d,(dict,list)) else json.dumps(d,ensure_ascii=False))
' "$1" 2>/dev/null
}

# expect <说明> <期望码> —— 不符即硬停，并把响应体打出来（否则只看到「失败」二字没法查）
expect() {
  if [ "$CODE" = "$2" ]; then ok "$1（HTTP ${CODE}）"; else
    bad "$1：期望 HTTP $2，实到 $CODE"
    printf '      %s\n' "$(printf '%s' "$BODY" | head -c 300)"
    die "$1"
  fi
}

# expect2xx <说明> —— 创建类端点返 201 是正确语义（back 的密钥生成就是 201），
# 只认 200 会把成功判成失败。第一版就这么误判过一次。
expect2xx() {
  case "$CODE" in
    2??) ok "$1（HTTP ${CODE}）" ;;
    *)   bad "$1：期望 2xx，实到 $CODE"
         printf '      %s\n' "$(printf '%s' "$BODY" | head -c 300)"
         die "$1" ;;
  esac
}

SUF=$(date +%s)
# 可用 Z2O_EMAIL_A/B + Z2O_PASSWORD 固定用户身份：租户 seeding 只能在服务器侧做
# （没有创建租户的 API），所以「注册 → 去服务器 seed → 重跑」这条路必须能复用同一批用户。
UA="${Z2O_EMAIL_A:-z2o-a-${SUF}@example.com}"
UB="${Z2O_EMAIL_B:-z2o-b-${SUF}@example.com}"
PW="${Z2O_PASSWORD:-Z2o!${SUF}xY}"

# ════════════════════════════════════════════════════════════
phase "Phase 0 · 环境自检（这一步不过，后面都不用跑）"
# ════════════════════════════════════════════════════════════
# 0.1 back 活着
req GET "$API/up"
[ "$CODE" = 200 ] && ok "back /up 可达（HTTP 200）" || bad "back /up 返回 $CODE —— 服务没起或 nginx 没配"

# 0.2 **业务面在不在**：这是 CPI-41 的判据。生产上曾只有框架 auth 面，
#     VPN/Monitor/Social 路由全 404，而 /up 照样 200 —— 只看 /up 会误判成「部署好了」
req GET "$API/api/v1/nodes"
case "$CODE" in
  401) ok "业务面已部署（/api/v1/nodes 返 401 未鉴权，说明路由存在）" ;;
  404) bad "/api/v1/nodes 404 —— 部署的 back **没有 VPN 业务面**（CPI-41 的形态）"
       printf '      判据：只有框架 auth 面时，/up 仍 200，所以不能只看 /up\n'
       die "业务面缺失" ;;
  *)   bad "/api/v1/nodes 返回 ${CODE}（期望 401）"; die "业务面异常" ;;
esac

# 0.3 心跳端点在（0→1 的「心跳有数据」全靠它）
req GET "$API/api/v1/monitor/node/info"
case "$CODE" in
  401|422|400) ok "节点面端点在（/api/v1/monitor/node/info → ${CODE}，未签名被拒是预期）" ;;
  404) bad "/api/v1/monitor/* 404 —— Monitor 域没部署（CPI-47 的表/路由缺失）"; die "Monitor 域缺失" ;;
  *)   bad "/api/v1/monitor/node/info 返回 ${CODE}（期望 401/422）"; die "节点面端点异常" ;;
esac

# 0.4 gateway 是**真的 gateway**，不是泛解析打到了 back。
#     这条必须查响应体形状：实测 gw.mynet.club 曾返回 back 的
#     {"name":"MyNetBackNew",...} 而 /healthz 是 404 —— 只看域名解析会误判
if [ -n "$GW" ]; then
  req GET "$GW/healthz"
  if [ "$CODE" = 200 ] && [ -n "$(jsonget data.conns)" ]; then
    ok "gateway 真在跑（/healthz 返回 conns=$(jsonget data.conns)）"
  else
    bad "gateway 不可用：/healthz → HTTP ${CODE}，body=$(printf '%s' "$BODY" | head -c 120)"
    printf '      若 body 是 {"name":"MyNetBackNew",…}，说明泛解析把 gw 打到了 back，\n'
    printf '      gateway 进程根本没起（或 nginx 的 proxy_pass 指错了端口）\n'
    die "gateway 不可用"
  fi
else
  printf '  ⚠ 未给 STAGE_GW —— 跳过 gateway 自检，Phase 3 的节点面也会跳\n'
fi

[ "$ONLY_PREFLIGHT" = 1 ] && { printf '\nPhase 0 完成：%s 通过 / %s 失败\n' "$pass" "$fail"; [ "$fail" -eq 0 ] || exit 1; exit 0; }

# ════════════════════════════════════════════════════════════
phase "Phase 1 · 用户面：注册 → 登录（cookie/token 会话，无密钥）"
# ════════════════════════════════════════════════════════════
# 限流等待：auth 端点有 throttle，可重入的验收脚本跑几轮就会撞 429。
# 撞了就等一会儿再试（有界，不无限等）—— 429 不是「失败」，是「稍后」。
wait_if_limited() { # <最多等几轮>
  _n=0
  while [ "$CODE" = 429 ] && [ "$_n" -lt "${1:-4}" ]; do
    _n=$((_n+1))
    printf '      429 限流，等 20s 后重试（%s/%s）\n' "$_n" "${1:-4}" >&2
    sleep 20
    return 1
  done
  return 0
}

reg_login() { # <name> <email> → 打印 token
  _try=0
  while :; do
    req POST "$API/api/v1/auth/register" "" \
      "{\"name\":\"$1\",\"email\":\"$2\",\"password\":\"$PW\",\"password_confirmation\":\"$PW\"}"
    wait_if_limited 4 && break
    _try=$((_try+1)); [ "$_try" -ge 4 ] && break
  done
  case "$CODE" in
    200|201) : ;;
    # 429 等过几轮仍在 → 明确报出来，别当成普通失败
    429)     printf '      注册持续 429（限流未解除）—— 等几分钟再跑，或换出口 IP\n' >&2; return 1 ;;
    # 422 通常是「email 已存在」—— 重跑同一批用户时的正常情况，落到登录即可。
    # 不区分会让脚本无法重入，而租户 seeding 恰恰要求重入。
    422)     printf '      注册返回 422（%s）—— 按「用户已存在」处理，转登录\n' "$(jsonget message)" >&2 ;;
    *)       printf '      注册失败 HTTP %s: %s\n' "$CODE" "$(printf '%s' "$BODY" | head -c 200)" >&2; return 1 ;;
  esac
  _try=0
  while :; do
    req POST "$API/api/v1/auth/login" "" "{\"email\":\"$2\",\"password\":\"$PW\"}"
    wait_if_limited 4 && break
    _try=$((_try+1)); [ "$_try" -ge 4 ] && break
  done
  [ "$CODE" = 200 ] || { printf '      登录失败 HTTP %s: %s\n' "$CODE" "$(printf '%s' "$BODY" | head -c 120)" >&2; return 1; }
  # token 字段名两种都试：auth_token（旧）/ token（框架）
  _t=$(jsonget auth_token); [ -n "$_t" ] || _t=$(jsonget token)
  [ -n "$_t" ] || _t=$(jsonget data.auth_token); [ -n "$_t" ] || _t=$(jsonget data.token)
  [ -n "$_t" ] || { printf '      登录 200 但取不到 token；body=%s\n' "$(printf '%s' "$BODY" | head -c 200)" >&2; return 1; }
  printf '%s' "$_t"
}

TA=$(reg_login "Z2oA" "$UA") || die "用户 A 注册/登录失败"
TB=$(reg_login "Z2oB" "$UB") || die "用户 B 注册/登录失败"
ok "两个用户注册并登录成功（token 长度 A=${#TA} B=${#TB}）"

# 会话真的可用：拿 token 打一个需要鉴权的端点
req GET "$API/api/v1/auth/me" "$TA"
expect "token 可用（/auth/me）" 200

# ════════════════════════════════════════════════════════════
phase "Phase 1b · 租户绑定（0→1 链上唯一需要服务器侧动作的一步）"
# ════════════════════════════════════════════════════════════
# register 后 tenant_id 为空，而 nodes/social 全都要租户上下文。
# **没有创建租户的 API，也不自动绑定**（实测：routes 里只有
# GET /api/v1/users/{id}/tenants，无 POST；register 流程不写 tenant_id）。
# 所以这一步只能在服务器侧做 —— 脚本检测到没绑就给出确切命令并停，
# 而不是像 t23_social.sh 那样硬编码本机路径去 bootstrap Laravel（打远端时那条路不存在）。
req GET "$API/api/v1/mynet/stats" "$TA" "X-Tenant-ID: $TENANT"
NEED_SEED=0
case "$CODE" in
  200) ok "租户上下文可用（/mynet/stats → 200）" ;;
  403|422|500) NEED_SEED=1 ;;
  *)   NEED_SEED=1 ;;
esac
if [ "$NEED_SEED" = 1 ]; then
  printf '  ⚠ 用户还没绑到租户 %s（/mynet/stats → HTTP %s）\n' "$TENANT" "$CODE"
  cat <<EOF

  在 **staging 服务器**上跑一次 seeding（本脚本打远端，没法替你做）：
    cd /data/app/mynet.club/stage-api
    php artisan tinker --execute='
      use MultiTenantSaas\\Modules\\Auth\\Models\\User;
      use Illuminate\\Support\\Facades\\DB;
      \$t = '"$TENANT"';
      foreach (["'"$UA"'","'"$UB"'"] as \$e) {
        \$u = User::where("email", \$e)->first();
        if (!\$u) { echo "缺用户 \$e\\n"; continue; }
        \$u->tenant_id = \$t; \$u->save();
        DB::table("tenant_users")->updateOrInsert(
          ["user_id" => \$u->user_id, "tenant_id" => \$t],
          ["is_active" => 1, "role" => "end_user", "created_at" => now(), "updated_at" => now()]);
        echo "bound {\$u->user_id}\\n";
      }'
  然后带同一批用户重跑（注册那步会 422，脚本按「已存在」转登录，可重入）：
    Z2O_EMAIL_A='"$UA"' Z2O_EMAIL_B='"$UB"' Z2O_PASSWORD='<上面的口令>' \
      STAGE_API=… STAGE_GW=… sh scripts/e2e/zero_to_one.sh
EOF
  die "需要服务器侧 seeding 才能继续"
fi

# ════════════════════════════════════════════════════════════
phase "Phase 2 · 建节点 → 生成密钥"
# ════════════════════════════════════════════════════════════
mknode() { # <token> <name> <vpn_type> → node_id
  req POST "$API/api/v1/nodes/create" "$1" \
    "{\"node_name\":\"$2\",\"vpn_type\":\"$3\",\"tenant_id\":$TENANT}" "X-Tenant-ID: $TENANT"
  [ "$CODE" = 200 ] || [ "$CODE" = 201 ] || { printf '      建节点失败 HTTP %s: %s\n' "$CODE" "$(printf '%s' "$BODY" | head -c 200)" >&2; return 1; }
  _n=$(jsonget data.node_id); [ -n "$_n" ] || _n=$(jsonget node_id)
  [ -n "$_n" ] || { printf '      建节点 200 但取不到 node_id；body=%s\n' "$(printf '%s' "$BODY" | head -c 200)" >&2; return 1; }
  printf '%s' "$_n"
}
NA=$(mknode "$TA" "z2o-a-$SUF" gnb) || die "A 建节点失败"
NB=$(mknode "$TB" "z2o-b-$SUF" gnb) || die "B 建节点失败"
ok "两个节点已建（A=$NA B=${NB}）"

# 密钥：这一步产出的 algorithm 字段是 CPI-37 的现场 ——
# 标错会让节点面全 401，且症状是「鉴权失败」而不是「algorithm 标错了」
req POST "$API/api/v1/nodes/$NA/keys" "$TA" '{}' "X-Tenant-ID: $TENANT"
expect2xx "A 节点密钥已生成"
KEYALG=$(jsonget data.key_type); [ -n "$KEYALG" ] || KEYALG=$(jsonget key_type)
printf '  ℹ A 的密钥类型回显：%s\n' "${KEYALG:-<未回显>}"
# 私钥不得出现在响应里（AGENTS.md §3 红线 / P0-4）
case "$BODY" in
  *private_key*) bad "密钥响应里出现了 private_key —— 私钥出口未收口（§3 红线）"; die "私钥泄露" ;;
  *) ok "密钥响应不含 private_key（P0-4 收口有效）" ;;
esac
req POST "$API/api/v1/nodes/$NB/keys" "$TB" '{}' "X-Tenant-ID: $TENANT"
expect2xx "B 节点密钥已生成"

# ════════════════════════════════════════════════════════════
phase "Phase 3 · 社区链接：加好友 → 接受"
# ════════════════════════════════════════════════════════════
req POST "$API/api/v1/social/search-users" "$TA" "{\"query\":\"$UB\"}" "X-Tenant-ID: $TENANT"
expect2xx "搜到用户 B"
UIDB=$(jsonget data.0.user_id); [ -n "$UIDB" ] || UIDB=$(jsonget data.0.id)
[ -n "$UIDB" ] || die "搜索结果里取不到 B 的 user_id；body=$(printf '%s' "$BODY" | head -c 200)"
ok "B 的 user_id=$UIDB"

req POST "$API/api/v1/social/friend-request" "$TA" \
  "{\"target_id\":$UIDB,\"message\":\"z2o\"}" "X-Tenant-ID: $TENANT"
case "$CODE" in
  200|201) ok "好友申请已发出" ;;
  # 409 = 已经是好友 / 申请已存在。这是**正确的业务语义**（CPI-32 把业务拒绝从 500
  # 改成 409/422，这里正好在真环境验到了）。重跑时必须当「已完成」继续，否则脚本不可重入。
  409)     ok "好友关系已存在（HTTP 409 —— 业务拒绝返 409 而非 500，CPI-32 的修复在真环境生效）" ;;
  *)       bad "好友申请失败 HTTP ${CODE}：$(printf '%s' "$BODY" | head -c 160)"; die "好友申请" ;;
esac

req GET "$API/api/v1/social/data" "$TB" "" "X-Tenant-ID: $TENANT"
expect "B 能看到待处理申请" 200
# 实测响应形状是 data.pending_requests[]（不是 friend_requests）—— 逐个候选试，
# 别把「我猜的字段名」当成契约。取不到就把 body 打出来，让人看见真实形状。
RID=""
for _p in data.pending_requests.0.request_id data.pending_requests.0.id \
          data.friend_requests.0.request_id data.friend_requests.0.id; do
  RID=$(jsonget "$_p"); [ -n "$RID" ] && break
done
[ -n "$RID" ] || die "取不到 request_id；body=$(printf '%s' "$BODY" | head -c 250)"
req POST "$API/api/v1/social/friend-request/accept" "$TB" \
  "{\"request_id\":$RID}" "X-Tenant-ID: $TENANT"
case "$CODE" in 200|201) ok "B 已接受，好友关系建立（request_id=${RID}）" ;; *) bad "接受失败 HTTP $CODE"; die "接受好友" ;; esac

# ════════════════════════════════════════════════════════════
phase "Phase 4 · 连接授权 → bundle 下发"
# ════════════════════════════════════════════════════════════
req POST "$API/api/v1/route/send-request" "$TA" \
  "{\"source_node_id\":$NA,\"target_node_id\":$NB,\"target_user_id\":$UIDB,\"vpn_type\":\"gnb\",\"tenant_id\":$TENANT,\"message\":\"z2o\"}" \
  "X-Tenant-ID: $TENANT"
case "$CODE" in
  200|201) ok "连接申请已发出" ;;
  # 重跑时「该连接已建立/已申请」是 409，属正确业务语义（CPI-32 的成果），继续往下走
  409)     ok "连接申请已存在（HTTP 409 —— 业务拒绝返 409 而非 500，CPI-32 生效）" ;;
  422)     bad "连接申请被校验拒（HTTP 422）：$(jsonget message)"; die "连接申请" ;;
  *) bad "连接申请失败 HTTP ${CODE}"; die "连接申请" ;;
esac

req GET "$API/api/v1/route/requests" "$TB" "" "X-Tenant-ID: $TENANT"
expect "B 能列到连接申请" 200

# ════════════════════════════════════════════════════════════
phase "Phase 5 · 节点面：签名请求必须过（CPI-37 的现形点）"
# ════════════════════════════════════════════════════════════
# 这一段要真起 mynetd（有节点私钥才能签），所以只在给了节点侧参数时跑。
if [ -n "${NODE_HOME:-}" ] && [ -n "$GW" ]; then
  printf '  ℹ 需要真 mynetd + 节点私钥，见 zero_to_one_node.sh（Phase 5/6 在节点机上跑）\n'
else
  printf '  ⚠ 已跳过：Phase 5/6 需要在**节点机**上跑（要节点私钥签名 + 起 mynetd）。\n'
  printf '    未给 NODE_HOME/STAGE_GW 时本脚本只验到 Phase 4（用户面 + 社区链接）。\n'
  printf '    ⚠️ 这不是通过 —— 节点面（CPI-37 全 401 的那一类）与真互通都还没验。\n'
fi

printf '\n════════ 0→1 验收：%s 通过 / %s 失败 ════════\n' "$pass" "$fail"
printf '已验到：环境自检 · 注册登录 · 租户上下文 · 建节点 · 密钥（含私钥不出口）· 好友 · 连接申请\n'
printf '未验（需节点机）：节点面签名（CPI-37）· bundle 真下发 · 两节点真互通 · 心跳落库\n'
[ "$fail" -eq 0 ] || exit 1
exit 0
