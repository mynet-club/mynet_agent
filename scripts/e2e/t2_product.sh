#!/bin/sh
# T2-7/T2-1..3：产品闭环冒烟（本地后端）
# 前置：php artisan serve --port=8080 + sqlite schema
set -e
API="${API:-http://127.0.0.1:8080}"
EMAIL="t2-$(date +%s)@example.com"
fail=0
log() { echo "[T2] $*"; }

log "1) register+login"
curl -sS -X POST "$API/api/v1/auth/register" -H 'Content-Type: application/json' \
  -d "{\"name\":\"T2\",\"email\":\"$EMAIL\",\"password\":\"Password123!\",\"password_confirmation\":\"Password123!\"}" >/dev/null
TOKEN=$(curl -sS -X POST "$API/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"Password123!\"}" | sed -n 's/.*"auth_token":"\([^"]*\)".*/\1/p')
[ -n "$TOKEN" ] && log "  ✓ token" || { log "  ✗ token"; exit 1; }

log "2) create node"
code=$(curl -sS -o /tmp/t2-node.json -w '%{http_code}' -X POST "$API/api/v1/nodes/create" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"node_name":"t2-a","vpn_type":"wireguard"}')
log "  · create $code $(head -c 120 /tmp/t2-node.json)"

log "3) list nodes"
curl -sS "$API/api/v1/nodes" -H "Authorization: Bearer $TOKEN" | head -c 200
echo
log "T2 smoke done (token=${TOKEN:0:10}...)"
