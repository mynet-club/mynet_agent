#!/bin/sh
# T2-3：好友 → 连接请求 → 授权（HTTP 全链）
set -e
API="${API:-http://127.0.0.1:8080}"
TENANT=1000000000000001
SUF=$(date +%s)
UA="t23a-$SUF@example.com"
UB="t23b-$SUF@example.com"
log() { echo "[T2-3] $*"; }

reg() {
  curl -sS -X POST "$API/api/v1/auth/register" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$1\",\"email\":\"$2\",\"password\":\"Password123!\",\"password_confirmation\":\"Password123!\"}" >/dev/null
  curl -sS -X POST "$API/api/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$2\",\"password\":\"Password123!\"}" | sed -n 's/.*"auth_token":"\([^"]*\)".*/\1/p'
}
auth() { tok=$1; shift; curl -sS -H "Authorization: Bearer $tok" -H "X-Tenant-ID: $TENANT" "$@"; }

TA=$(reg "UserA" "$UA")
TB=$(reg "UserB" "$UB")
log "1) tokens A=${#TA} B=${#TB}"

# 绑租户（register 后 tenant 为空）
php <<PHP
<?php
require "/Users/arthur/Devel/WorkSpaceAI/mynet-club/mynet_back/vendor/autoload.php";
\$app = require "/Users/arthur/Devel/WorkSpaceAI/mynet-club/mynet_back/bootstrap/app.php";
\$app->make(Illuminate\Contracts\Console\Kernel::class)->bootstrap();
use MultiTenantSaas\Modules\Auth\Models\User;
use Illuminate\Support\Facades\DB;
foreach (["$UA","$UB"] as \$e) {
  \$u = User::where('email', \$e)->first();
  if (!\$u) continue;
  \$u->tenant_id = $TENANT;
  \$u->save();
  DB::table('tenant_users')->updateOrInsert(
    ['user_id' => \$u->user_id, 'tenant_id' => $TENANT],
    ['is_active' => 1, 'role' => 'end_user', 'created_at' => now(), 'updated_at' => now()]
  );
  echo "bound {\$u->user_id}\n";
}
PHP

log "2) 节点"
NA=$(auth "$TA" -X POST "$API/api/v1/nodes/create" -H 'Content-Type: application/json' \
  -d '{"node_name":"t23-a","vpn_type":"wireguard","tenant_id":'"$TENANT"'}' | sed -n 's/.*"node_id":\([0-9]*\).*/\1/p')
NB=$(auth "$TB" -X POST "$API/api/v1/nodes/create" -H 'Content-Type: application/json' \
  -d '{"node_name":"t23-b","vpn_type":"wireguard","tenant_id":'"$TENANT"'}' | sed -n 's/.*"node_id":\([0-9]*\).*/\1/p')
log "  A=$NA B=$NB"

log "3) 密钥"
auth "$TA" -X POST "$API/api/v1/nodes/$NA/keys" -H 'Content-Type: application/json' -d '{}' | head -c 80; echo
auth "$TB" -X POST "$API/api/v1/nodes/$NB/keys" -H 'Content-Type: application/json' -d '{}' | head -c 80; echo

log "4) 好友"
UID_B=$(auth "$TA" -X POST "$API/api/v1/social/search-users" -H 'Content-Type: application/json' \
  -d "{\"query\":\"$UB\"}" | sed -n 's/.*"user_id":\([0-9]*\).*/\1/p' | head -1)
UID_A=$(auth "$TB" -X POST "$API/api/v1/social/search-users" -H 'Content-Type: application/json' \
  -d "{\"query\":\"$UA\"}" | sed -n 's/.*"user_id":\([0-9]*\).*/\1/p' | head -1)
log "  UID_A=$UID_A UID_B=$UID_B"
auth "$TA" -X POST "$API/api/v1/social/friend-request" -H 'Content-Type: application/json' \
  -d "{\"target_id\":$UID_B,\"message\":\"t23\"}" | head -c 180; echo
# 列出收到的申请取 request_id
RID=$(auth "$TB" "$API/api/v1/social/data" | sed -n 's/.*"request_id":\([0-9]*\).*/\1/p' | head -1)
log "  request_id=$RID"
if [ -n "$RID" ]; then
  auth "$TB" -X POST "$API/api/v1/social/friend-request/accept" -H 'Content-Type: application/json' \
    -d "{\"request_id\":$RID}" | head -c 180; echo
fi

log "5) 连接请求"
auth "$TA" -X POST "$API/api/v1/route/send-request" -H 'Content-Type: application/json' \
  -d "{\"source_node_id\":$NA,\"target_node_id\":$NB,\"target_user_id\":$UID_B,\"vpn_type\":\"gnb\",\"tenant_id\":$TENANT,\"message\":\"t23\"}" | head -c 220; echo

log "6) 列连接请求（B 侧）"
auth "$TB" "$API/api/v1/route/requests" | head -c 250; echo
log "T2-3 done"
