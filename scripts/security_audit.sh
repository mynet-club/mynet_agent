#!/bin/sh
# R5-6 安全复检（可重复）
set -e
AGENT=$(cd "$(dirname "$0")/.." && pwd)
CLUB=$(cd "$AGENT/.." && pwd)
BACK="$CLUB/mynet_api"
fail=0
ok()  { echo "  ✓ $1"; }
bad() { echo "  ✗ $1"; fail=1; }

# C1-09：缺仓/缺关键文件必须**硬失败**，不能 grep 空当通过（假绿）。
for req in "$BACK" "$CLUB/mynet_web" "$BACK/app/Modules/VPN/Models/NodeKey.php" \
           "$AGENT/mynetd/localapi/server.go" "$AGENT/gateway/server.go"; do
  if [ ! -e "$req" ]; then
    echo "  ✗ 缺必需路径（审计不得假绿）: $req"
    echo "SECURITY AUDIT FAIL"
    exit 1
  fi
done

echo "== A 凭据 =="
if grep -rn 'Lyt@' "$AGENT" "$BACK" "$CLUB/mynet_web" 2>/dev/null | grep -v vendor | grep -v node_modules | grep -v .git | grep -q .; then
  bad "明文密码残留"
else
  ok "无明文密码"
fi

echo "== B 私钥出口 =="
nk="$BACK/app/Modules/VPN/Models/NodeKey.php"
if grep -E "protected \$fillable" "$nk" | grep -q private_key; then
  bad "private_key 进 fillable"
else
  ok "private_key 不在 fillable"
fi
if grep -q "'private_key'" "$nk" && grep -q "hidden" "$nk"; then
  ok "NodeKey.hidden 含 private_key"
else
  bad "NodeKey 未 hidden private_key"
fi

echo "== C DNS rebinding / cookie =="
SVR="$AGENT/mynetd/localapi/server.go"
grep -q 'SameSiteStrictMode' "$SVR" && grep -q 'HttpOnly' "$SVR" && ok "cookie HttpOnly+Strict" || bad "cookie 属性缺失"
grep -q 'checkHost' "$SVR" && grep -q 'checkOrigin' "$SVR" && ok "Host/Origin 校验" || bad "缺 Host/Origin"

echo "== D 重放 =="
grep -q 'TIMESTAMP_WINDOW' "$BACK/app/Modules/VPN/Middleware/NodeHeartbeatAuthMiddleware.php" && ok "心跳时间窗" || bad "心跳无时间窗"

echo "== E 鉴权分层 =="
GW="$AGENT/gateway/server.go"
grep -q '不鉴权' "$GW" && ok "gateway 不鉴权" || bad "gateway 鉴权语义"
grep -q 'X-Node-Verified' "$GW" && ok "C-4 置位信号" || bad "缺 X-Node-Verified"

echo "== F guard =="
(cd "$AGENT" && sh .githooks/guard.sh >/dev/null) && ok "agent guard" || bad "agent guard"
(cd "$BACK" && sh .githooks/guard.sh >/dev/null) && ok "后端 guard" || bad "后端 guard"

echo "== G 业务模型无驱动术语 =="
if grep -RInE 'AllowedIPs|node\.conf' "$BACK/app/Modules/VPN/Models" 2>/dev/null | grep -q .; then
  bad "业务模型含驱动术语"
else
  ok "业务模型无 AllowedIPs/node.conf"
fi

if [ $fail -eq 0 ]; then echo "SECURITY AUDIT PASS"; else echo "SECURITY AUDIT FAIL"; exit 1; fi
