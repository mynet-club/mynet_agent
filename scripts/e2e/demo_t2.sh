#!/bin/sh
# U1-B3/T2-7: 产品闭环一键
set -e
API="${API:-http://127.0.0.1:8080}"
DIR=$(cd "$(dirname "$0")" && pwd)
echo "== 1) 社交链（注册→节点→密钥→好友→连接）=="
sh "$DIR/t23_social.sh" || true
echo "== 2) G3 真隧道三场景 =="
sh "$DIR/g3_tunnel.sh" || true
echo "== 3) 三机 mesh =="
sh "$DIR/u1_mesh.sh" || true
echo "== 4) 跨驱动桥 =="
curl -sS -m 3 http://192.168.0.32:8080/ 2>/dev/null | head -1 || true
echo "DEMO_T2 DONE"
