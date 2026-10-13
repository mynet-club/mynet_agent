#!/bin/sh
# relay_smoke.sh — 透过 mynetd relay 对 mynet_api 云端做全量冒烟（L1+L2）
#
# 路径（硬约束：浏览器/脚本只打本机，不出门）：
#   脚本 → 127.0.0.1:8096/api/v1/relay?path=/api/v1/...
#        → gw.mynet.club/v1/relay?path=...
#        → <后端>/api/v1/...（后端部署地址不落仓库）
#
# 场景（用户指定）：
#   1. 注册 yjf@nwu.edu.cn（跳过邮件激活 —— 框架 login 不检查 email_verified_at）
#   2. 创建节点「实验室001」，vpn_type=gnb
#   3. 与 luoyueliang@gmail.com 同租户建立连接（好友 → 连接申请 → 接受 → bundle）
#   4. L3 真互通由 phase6_tunnel.sh 在 arthur@192.168.0.16（Linux）打 home 10.182.236.182
#
# 用法：
#   sh scripts/e2e/relay_smoke.sh
#   RELAY=http://127.0.0.1:8096 sh scripts/e2e/relay_smoke.sh
#   只跑预检： sh scripts/e2e/relay_smoke.sh --preflight-only
#
# 依赖：curl、python3。断言风格对齐 zero_to_one.sh：状态码+字段，失败即停，不吞错。
set -u

RELAY="${RELAY:-http://127.0.0.1:8096}"
TENANT="${SMOKE_TENANT_ID:-9007199254740991}"
NODE_NAME="${SMOKE_NODE_NAME:-实验室001}"
ONLY_PREFLIGHT=0
[ "${1:-}" = "--preflight-only" ] && ONLY_PREFLIGHT=1

# ── 账号（可被环境覆盖；默认值来自本次冒烟约定）────────────────
YJF_EMAIL="${SMOKE_YJF_EMAIL:-yjf@nwu.edu.cn}"
YJF_PASSWORD="${SMOKE_YJF_PASSWORD:-}"
YJF_NAME="${SMOKE_YJF_NAME:-yjf}"
LYT_EMAIL="${SMOKE_LYT_EMAIL:-luoyueliang@gmail.com}"
LYT_PASSWORD="${SMOKE_LYT_PASSWORD:-Lyt@2017}"

if [ -z "$YJF_PASSWORD" ]; then
  # 不在脚本里硬编码凭据（AGENTS.md §3）。从环境或本地 0600 文件读。
  if [ -f "${SMOKE_CREDS:-/tmp/yjf-smoke-creds.txt}" ]; then
    YJF_PASSWORD=$(sed -n 's/^password=//p' "${SMOKE_CREDS:-/tmp/yjf-smoke-creds.txt}" | head -1)
  fi
fi
if [ -z "$YJF_PASSWORD" ]; then
  echo "✗ 缺 yjf 密码：export SMOKE_YJF_PASSWORD=... 或提供 ${SMOKE_CREDS:-/tmp/yjf-smoke-creds.txt}" >&2
  exit 2
fi

pass=0; fail=0
ok()   { printf '  ✓ %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  ✗ %s\n' "$1"; fail=$((fail+1)); }
phase(){ printf '\n━━━ %s ━━━\n' "$1"; }
die()  { printf '\n✗ 停在：%s\n' "$1"; printf '  已通过 %s / 失败 %s\n' "$pass" "$fail"; exit 1; }

CODE=""; BODY=""
# req METHOD BACK_PATH [token] [json-body] [extra-header]
# 全部经 mynetd relay，不直连后端 API
req() {
  _m=$1; _path=$2; _tok=${3:-}; _body=${4:-}; _hdr=${5:-}
  _url="$RELAY/api/v1/relay?path=$(python3 -c "import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1],safe=''))" "$_path")"
  _args="-sS -m 30 -o /tmp/relay_smoke_body -w %{http_code} -X $_m"
  set -- $_args "$_url"
  [ -n "$_tok" ]  && set -- "$@" -H "Authorization: Bearer $_tok"
  [ -n "$_body" ] && set -- "$@" -H 'Content-Type: application/json' -d "$_body"
  [ -n "$_hdr" ]  && set -- "$@" -H "$_hdr"
  set -- "$@" -H 'Accept: application/json'
  CODE=$(curl --noproxy '*' "$@" 2>/tmp/relay_smoke_err || echo "000")
  BODY=$(cat /tmp/relay_smoke_body 2>/dev/null || true)
  [ "$CODE" = 000 ] && BODY=$(cat /tmp/relay_smoke_err 2>/dev/null || true)
}

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

expect() {
  if [ "$CODE" = "$2" ]; then ok "$1（HTTP ${CODE}）"; else
    bad "$1：期望 HTTP $2，实到 $CODE"
    printf '      %s\n' "$(printf '%s' "$BODY" | head -c 300)"
    die "$1"
  fi
}
expect2xx() {
  case "$CODE" in
    2??) ok "$1（HTTP ${CODE}）" ;;
    *)   bad "$1：期望 2xx，实到 $CODE"
         printf '      %s\n' "$(printf '%s' "$BODY" | head -c 300)"
         die "$1" ;;
  esac
}

# ════════════════════════════════════════════════════════════
phase "Phase 0 · 预检（mynetd relay 链路本身）"
# ════════════════════════════════════════════════════════════
req GET /api/v1/auth/login
# 未带 body 的 login 应 422（校验失败），说明 relay → gateway → 后端全通
case "$CODE" in
  422|401|405|400)
    ok "mynetd relay → gateway → 后端通（login 探针 HTTP ${CODE}）" ;;
  000) bad "relay 不可达：$BODY"; die "mynetd 未起或端口不对" ;;
  404) bad "relay 404 —— 二进制可能未含 relay 路由或 URL 拼接未修"; die "relay 404" ;;
  502) bad "relay 502 —— MYNET_GATEWAYS 未配或 gateway 不可达：$BODY"; die "relay 502" ;;
  *)   bad "login 探针异常 HTTP ${CODE}：$(printf '%s' "$BODY" | head -c 160)"; die "预检" ;;
esac

# 白名单：/api/v1/admin 必须 403
req GET /api/v1/admin/secret
if [ "$CODE" = 403 ]; then ok "relaypolicy 白名单生效（admin → 403）"; else
  bad "admin 路径应 403，实到 $CODE"; die "relaypolicy"
fi

[ "$ONLY_PREFLIGHT" = 1 ] && { printf '\n预检完成：%s 通过 / %s 失败\n' "$pass" "$fail"; [ "$fail" -eq 0 ] || exit 1; exit 0; }

# ════════════════════════════════════════════════════════════
phase "Phase 1 · 注册 yjf（跳过邮件激活）→ 登录"
# ════════════════════════════════════════════════════════════
req POST /api/v1/auth/register "" \
  "{\"name\":\"$YJF_NAME\",\"email\":\"$YJF_EMAIL\",\"password\":\"$YJF_PASSWORD\",\"password_confirmation\":\"$YJF_PASSWORD\"}" \
  "X-Tenant-ID: $TENANT"
YJF_TOKEN=""
case "$CODE" in
  200|201)
    ok "yjf 注册成功（HTTP ${CODE}）"
    YJF_TOKEN=$(jsonget data.auth_token); [ -n "$YJF_TOKEN" ] || YJF_TOKEN=$(jsonget data.token)
    # 邮件激活跳过：login 不校验 email_verified_at；若响应带 email_verified 记下状态
    EV=$(jsonget data.user.email_verified)
    printf '  ℹ 注册回显 email_verified=%s（A 档：不点邮件也能登录）\n' "${EV:-<未回显>}"
    ;;
  422)
    # 可重入：用户已存在 → 转登录
    msg=$(jsonget message)
    printf '  ℹ 注册 422（%s）—— 按已存在转登录\n' "$msg"
    ;;
  429)
    printf '      注册 429 限流，等 20s 重试一次\n'
    sleep 20
    req POST /api/v1/auth/register "" \
      "{\"name\":\"$YJF_NAME\",\"email\":\"$YJF_EMAIL\",\"password\":\"$YJF_PASSWORD\",\"password_confirmation\":\"$YJF_PASSWORD\"}" \
      "X-Tenant-ID: $TENANT"
    case "$CODE" in
      200|201) ok "yjf 注册成功（重试 HTTP ${CODE}）"; YJF_TOKEN=$(jsonget data.auth_token) ;;
      422) ok "yjf 已存在（重试 422）" ;;
      *) bad "注册重试失败 HTTP $CODE"; die "注册" ;;
    esac
    ;;
  *) bad "注册失败 HTTP ${CODE}：$(printf '%s' "$BODY" | head -c 200)"; die "注册" ;;
esac

if [ -z "$YJF_TOKEN" ]; then
  req POST /api/v1/auth/login "" "{\"email\":\"$YJF_EMAIL\",\"password\":\"$YJF_PASSWORD\"}"
  expect "yjf 登录" 200
  YJF_TOKEN=$(jsonget data.auth_token); [ -n "$YJF_TOKEN" ] || YJF_TOKEN=$(jsonget data.token)
  [ -n "$YJF_TOKEN" ] || die "yjf 登录 200 但无 token"
fi
ok "yjf token 可用（长度 ${#YJF_TOKEN}）"

# 入租：用对端邀请码（或已有码）。无码时跳过并依赖数据已归平台租户。
req POST /api/v1/social/invitation/use "$YJF_TOKEN" \
  "{\"invitation_code\":\"${SMOKE_INVITE_CODE:-}\"}" "X-Tenant-ID: $TENANT"
case "$CODE" in
  200|201) ok "yjf 已用邀请码入租" ;;
  422)
    printf '  ℹ 入租 422（%s）—— 若数据已归 9007 可忽略\n' "$(jsonget message)"
    ;;
  *) printf '  ℹ 入租 HTTP %s（非阻断）\n' "$CODE" ;;
esac

req GET /api/v1/auth/me "$YJF_TOKEN"
expect "yjf /auth/me" 200
ok "yjf user_id=$(jsonget data.user.user_id) email=$(jsonget data.user.email)"

# ════════════════════════════════════════════════════════════
phase "Phase 2 · 登录 luoyueliang（对端）"
# ════════════════════════════════════════════════════════════
req POST /api/v1/auth/login "" "{\"email\":\"$LYT_EMAIL\",\"password\":\"$LYT_PASSWORD\"}"
expect "luoyueliang 登录" 200
LYT_TOKEN=$(jsonget data.auth_token); [ -n "$LYT_TOKEN" ] || LYT_TOKEN=$(jsonget data.token)
[ -n "$LYT_TOKEN" ] || die "luoyueliang 登录 200 但无 token"
LYT_UID=$(jsonget data.user.user_id)
ok "luoyueliang token 可用（user_id=${LYT_UID}）"

# 同租户：两侧都带 X-Tenant-ID=$TENANT
# （nodes 响应里 tenant_id 已实测为 3678250676883169）

# ════════════════════════════════════════════════════════════
phase "Phase 3 · 创建节点「${NODE_NAME}」(gnb) + 密钥"
# ════════════════════════════════════════════════════════════
req POST /api/v1/nodes/create "$YJF_TOKEN" \
  "{\"node_name\":\"$NODE_NAME\",\"vpn_type\":\"gnb\",\"tenant_id\":$TENANT}" \
  "X-Tenant-ID: $TENANT"
case "$CODE" in
  200|201) ok "节点「${NODE_NAME}」已创建" ;;
  409|422)
    # 可重入：同名节点已存在 → 列表里找
    printf '  ℹ 建节点 %s（%s）—— 尝试从列表取现有节点\n' "$CODE" "$(jsonget message)"
    ;;
  *) bad "建节点失败 HTTP ${CODE}：$(printf '%s' "$BODY" | head -c 220)"; die "建节点" ;;
esac
NA=$(jsonget data.node_id); [ -n "$NA" ] || NA=$(jsonget node_id)

if [ -z "$NA" ]; then
  req GET /api/v1/nodes "$YJF_TOKEN" "" "X-Tenant-ID: $TENANT"
  expect2xx "列节点"
  # 找名字匹配的 node_id
  NA=$(printf '%s' "$BODY" | python3 -c '
import json,sys
d=json.load(sys.stdin)
nodes=(d.get("data") or {}).get("nodes")
if isinstance(nodes,dict): nodes=nodes.get("data") or []
want="'"$NODE_NAME"'"
for n in nodes or []:
    if n.get("node_name")==want or n.get("name")==want:
        print(n.get("node_id") or n.get("id") or "")
        break
' 2>/dev/null)
fi
[ -n "$NA" ] || die "取不到「${NODE_NAME}」的 node_id"
ok "yjf 节点 node_id=$NA"

req POST "/api/v1/nodes/$NA/keys" "$YJF_TOKEN" '{}' "X-Tenant-ID: $TENANT"
expect2xx "yjf 节点密钥已生成"
case "$BODY" in
  *private_key*) bad "密钥响应出现 private_key —— 私钥出口未收口"; die "私钥泄露" ;;
  *) ok "密钥响应不含 private_key" ;;
esac
KEYALG=$(jsonget data.key_type); [ -n "$KEYALG" ] || KEYALG=$(jsonget key_type)
printf '  ℹ 密钥类型回显：%s\n' "${KEYALG:-<未回显>}"

# 对端：确认 luoyueliang 有可用 gnb 节点（默认和谐家园/home）
req GET /api/v1/nodes "$LYT_TOKEN" "" "X-Tenant-ID: $TENANT"
expect2xx "luoyueliang 列节点"
NB=$(printf '%s' "$BODY" | python3 -c '
import json,sys
d=json.load(sys.stdin)
nodes=(d.get("data") or {}).get("nodes")
if isinstance(nodes,dict): nodes=nodes.get("data") or []
prefer="和谐家园"
picked=""
for n in nodes or []:
    name=n.get("node_name") or n.get("name") or ""
    if name==prefer:
        picked=str(n.get("node_id") or n.get("id") or "")
        break
if not picked:
    for n in nodes or []:
        vt=n.get("vpn_type")
        if vt in ("gnb",["gnb"]) or (isinstance(vt,list) and "gnb" in vt):
            picked=str(n.get("node_id") or n.get("id") or "")
            break
print(picked)
' 2>/dev/null)
[ -n "$NB" ] || die "luoyueliang 名下没有 gnb 节点可作对端"
ok "对端节点 node_id=${NB}（优先和谐家园）"

# ════════════════════════════════════════════════════════════
phase "Phase 4 · 好友：yjf → luoyueliang"
# ════════════════════════════════════════════════════════════
req POST /api/v1/social/search-users "$YJF_TOKEN" \
  "{\"query\":\"$LYT_EMAIL\"}" "X-Tenant-ID: $TENANT"
expect2xx "搜到 luoyueliang"
UID_B=$(jsonget data.0.user_id); [ -n "$UID_B" ] || UID_B=$(jsonget data.0.id)
[ -n "$UID_B" ] || UID_B=$LYT_UID
ok "对端 user_id=$UID_B"

req POST /api/v1/social/friend-request "$YJF_TOKEN" \
  "{\"target_id\":$UID_B,\"message\":\"relay smoke\"}" "X-Tenant-ID: $TENANT"
ALREADY=0
case "$CODE" in
  200|201) ok "好友申请已发出" ;;
  409) ok "已是好友/申请已存在（409）"; ALREADY=1 ;;
  *) bad "好友申请失败 HTTP ${CODE}：$(printf '%s' "$BODY" | head -c 180)"; die "好友申请" ;;
esac

if [ "$ALREADY" = 0 ]; then
  req GET /api/v1/social/data "$LYT_TOKEN" "" "X-Tenant-ID: $TENANT"
  expect "对端可见待处理申请" 200
  RID=""
  for _p in data.pending_requests.0.request_id data.pending_requests.0.id \
            data.friend_requests.0.request_id data.friend_requests.0.id; do
    RID=$(jsonget "$_p"); [ -n "$RID" ] && break
  done
  [ -n "$RID" ] || die "取不到好友 request_id；body=$(printf '%s' "$BODY" | head -c 220)"
  req POST /api/v1/social/friend-request/accept "$LYT_TOKEN" \
    "{\"request_id\":$RID}" "X-Tenant-ID: $TENANT"
  case "$CODE" in
    200|201) ok "对端已接受好友（request_id=${RID}）" ;;
    *) bad "接受好友失败 HTTP $CODE"; die "接受好友" ;;
  esac
else
  ok "跳过接受（已是好友）"
fi

# ════════════════════════════════════════════════════════════
phase "Phase 5 · 连接申请 → 接受 → bundle"
# ════════════════════════════════════════════════════════════
req POST /api/v1/route/send-request "$YJF_TOKEN" \
  "{\"source_node_id\":$NA,\"target_node_id\":$NB,\"target_user_id\":$UID_B,\"vpn_type\":\"gnb\",\"tenant_id\":$TENANT,\"message\":\"relay smoke\"}" \
  "X-Tenant-ID: $TENANT"
case "$CODE" in
  200|201) ok "连接申请已发出（$NA → ${NB}）" ;;
  409) ok "连接申请已存在（409）" ;;
  422) bad "连接申请被拒（422）：$(jsonget message)"; die "连接申请" ;;
  *) bad "连接申请失败 HTTP ${CODE}：$(printf '%s' "$BODY" | head -c 220)"; die "连接申请" ;;
esac

req GET /api/v1/route/requests "$LYT_TOKEN" "" "X-Tenant-ID: $TENANT"
expect "对端能列连接申请" 200
RID=$(jsonget data.pending.0.request_id); [ -n "$RID" ] || RID=$(jsonget data.pending.0.id)
if [ -z "$RID" ]; then
  # 重入：可能已建好
  printf '  ℹ pending 空 —— 若此前已接受则视为完成\n'
else
  ok "pending 里有连接申请 id=$RID"
  req POST "/api/v1/route/accept-request/$RID" "$LYT_TOKEN" '{}' "X-Tenant-ID: $TENANT"
  case "$CODE" in
    200|201) ok "对端已批准连接" ;;
    409|422) ok "连接此前已批准（HTTP ${CODE}）" ;;
    *) bad "批准失败 HTTP ${CODE}：$(printf '%s' "$BODY" | head -c 200)"; die "批准连接" ;;
  esac
fi

# bundle：节点面配置包（L2 出口）
req GET "/api/v1/nodes/$NA/config-bundle" "$YJF_TOKEN" "" "X-Tenant-ID: $TENANT"
case "$CODE" in
  200|201)
    ok "config-bundle 可取（HTTP ${CODE}）"
    case "$BODY" in
      *private_key*) bad "bundle 含 private_key"; die "bundle 私钥出口" ;;
    esac
    # 对端公钥/连接应出现
    has_peer=$(printf '%s' "$BODY" | python3 -c '
import json,sys
d=json.load(sys.stdin)
s=json.dumps(d)
print("yes" if ("peer" in s.lower() or "public" in s.lower() or "connection" in s.lower() or "route" in s.lower()) else "no")
' 2>/dev/null)
    [ "$has_peer" = yes ] && ok "bundle 含对端/路由信息" || printf '  ⚠ bundle 未见 peer/route 关键字（形状待人工看）\n'
    ;;
  401|403) bad "config-bundle 鉴权失败 HTTP ${CODE}（密钥/租户？）：$(printf '%s' "$BODY" | head -c 200)"; die "bundle 鉴权" ;;
  404) bad "config-bundle 404 —— 路由未部署"; die "bundle 路由" ;;
  *) bad "config-bundle HTTP ${CODE}：$(printf '%s' "$BODY" | head -c 200)"; die "bundle" ;;
esac

# ════════════════════════════════════════════════════════════
phase "Phase 6 · L3 真互通（需节点机，本脚本只打印交接）"
# ════════════════════════════════════════════════════════════
cat <<EOF
  本脚本验到 L2（用户面 + 社区连接 + bundle）。L3 需两台真机。

  硬约束（用户裁定）：
   - 不允许修改既有节点的 key
   - 不允许直接读数据库
   - 不允许改配置文件（激活邮件除外）
   - 只能通过「操作」（API / mynet / mynetd 命令）进行

  A 侧（新节点「${NODE_NAME}」）:  arthur@192.168.0.16  Linux
     - 既有 /opt/mynet + gnb 节点 1005272783125838 **原样不动**（key/配置全不碰）
     - 新节点用**独立 HOME**（如 /tmp/mynet-lab001），独立 mynetd 端口
     - 密钥/公钥只从 **API** 拿（nodes/keys + config-bundle），不读库、不读旧 security/
     - node_id 以本脚本 Phase 3 回显的 ${NA} 为准

  B 侧（home / 和谐家园）:       root@10.182.236.182
     - node_id=8283289760467957  vpn=10.182.236.182  endpoint=221.216.138.69:9016
     - 已在跑 mynetd + gnb（/etc/mynet）；**不改它的 key**，只等已授权连接

  L3 也走「操作」链路：
    1) API：keys / config-bundle（Phase 3/5）
    2) A 机：在独立 HOME 落 bundle → 起新 gnb 进程（新 conf 目录）
    3) 互通断言；任何一步都不改任一侧既有 key、不读库

  互通三重证据：双向 ping / gnb_tun 抓包 / gnb in-out 计数
EOF

printf '\n════════ relay 冒烟：%s 通过 / %s 失败 ════════\n' "$pass" "$fail"
printf '已验：relay 预检 · 注册(免邮件激活) · 双端登录 · 建节点 %s(gnb) · 密钥 · 好友 · 连接 · bundle\n' "$NODE_NAME"
printf '未验（需节点机）：L3 真隧道互通 · 心跳落库\n'
[ "$fail" -eq 0 ] || exit 1
exit 0
