#!/bin/sh
# U1 过夜总控：一键跑全部验收
DIR=$(cd "$(dirname "$0")" && pwd)
fail=0
run() { echo ""; echo "########## $1 ##########"; shift; "$@" || fail=1; }

run "G3 三场景" sh "$DIR/g3_tunnel.sh"
run "三机 mesh" sh "$DIR/u1_mesh.sh"
run "社交/连接链" sh "$DIR/t23_social.sh"
run "安全复检" sh "$DIR/../security_audit.sh"
run "产品 demo" sh "$DIR/demo_t2.sh"

echo ""
if [ $fail -eq 0 ]; then
  echo "======== U1 ALL PASS ========"
else
  echo "======== U1 SOME FAILED ========"
  exit 1
fi
