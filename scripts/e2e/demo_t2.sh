#!/bin/sh
# U1-B3: 产品闭环一键（注册→节点→密钥→好友→连接）
set -e
API="${API:-http://127.0.0.1:8080}"
echo "[demo] 调用 t23_social.sh"
sh "$(dirname "$0")/t23_social.sh"
echo "[demo] 调用 g3_tunnel.sh"
sh "$(dirname "$0")/g3_tunnel.sh" || true
echo "[demo] 调用 u1_mesh.sh"
sh "$(dirname "$0")/u1_mesh.sh" || true
echo "[demo] T2 产品链演示完成"
