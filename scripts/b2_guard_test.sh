#!/bin/sh
# b2_guard_test.sh — B2 红线变异测试（G3.c）
#
# 三类变异：
#   1) 注释里的包名不误报
#   2) 真 import 必须拦下
#   3) 传递依赖（go list -deps）必须能扫出根 mynet/detector 等
#
# 用法：sh scripts/b2_guard_test.sh
set -u

ROOT=$(cd "$(dirname "$0")/.." && pwd)
GUARD="$ROOT/.githooks/guard.sh"
pass=0; fail=0

check() {
  _name=$1; _want=$2; _needle=$3; _body=$4
  d=$(mktemp -d)
  mkdir -p "$d/mynetd" "$d/.githooks"
  cp "$GUARD" "$d/.githooks/guard.sh"
  ( cd "$d" && git init -q . \
    && git add .githooks/guard.sh \
    && git -c user.name=t -c user.email=t@t commit -q -m init )
  # 变异内容落在**新增行**（added_in_file 扫 git diff --unified=0 的 + 行）
  printf '%s\n' "$_body" > "$d/mynetd/probe.go"
  out=$(cd "$d" && sh .githooks/guard.sh 2>&1)
  rc=$?
  rm -rf "$d"
  if [ "$_want" = "pass" ]; then
    if [ $rc -eq 0 ] && printf '%s' "$out" | grep -q "未依赖"; then
      printf '  ✓ %s\n' "$_name"; pass=$((pass+1))
    else
      printf '  ✗ %s（期望通过且不含 %s）\n%s\n' "$_name" "$_needle" "$out"
      fail=$((fail+1))
    fi
  else
    if [ $rc -ne 0 ] && printf '%s' "$out" | grep -q "不得依赖"; then
      printf '  ✓ %s\n' "$_name"; pass=$((pass+1))
    else
      printf '  ✗ %s（期望拦下且含 %s）\n%s\n' "$_name" "$_needle" "$out"
      fail=$((fail+1))
    fi
  fi
}

echo "▸ B2 变异测试（被测：${GUARD}）"

# 1) 注释不误报
check "注释里的 mynet/detector 不算违规" pass "B2" '
package mynetd
// 见 mynet/detector 与 shared/config 的说明，这里只是注释。
func Note() {}
'

# 2) 真 import 必须拦
check "import 根 mynet 包 → 拦" fail "B2 mynetd" '
package mynetd
import "github.com/mynet-club/mynet_agent/mynet"
func Use() { _ = mynet.Run }
'

check "import shared/config → 拦" fail "B2 mynetd" '
package mynetd
import "github.com/mynet-club/mynet_agent/shared/config"
func Use() { _ = config.LoadProxyIntent }
'

check "import mynet/detector → 拦" fail "B2 mynetd" '
package mynetd
import "github.com/mynet-club/mynet_agent/mynet/detector"
func Use() { _ = detector.SystemDetector(nil) }
'

check "import gopsutil → 拦" fail "B2 mynetd" '
package mynetd
import "github.com/shirou/gopsutil/cpu"
func Use() { _, _ = cpu.Percent(0, false) }
'

# 3) 允许项
check "mynet/keys 密钥原语允许" pass "B2" '
package mynetd
import "github.com/mynet-club/mynet_agent/mynet/keys"
func Use() { _, _ = keys.GenerateX25519FromPrivateHex("00") }
'

check "shared/creds 会话原语允许" pass "B2" '
package mynetd
import "github.com/mynet-club/mynet_agent/shared/creds"
func Use() { _ = creds.DeviceFingerprint() }
'

check "GUARD-OK 行豁免" pass "B2" '
package mynetd
import "github.com/mynet-club/mynet_agent/mynet" // GUARD-OK 组合根测试
func Use() {}
'

# 4) 传递依赖扫描（真仓）
echo "▸ 传递依赖扫描（go list -deps）"
# 组合根（cmd/mynetd）允许根 mynet/shared/config/keys/creds（GUARD-OK 注释的 wiring）；
# **禁止**系统能力子包 detector/tools/apply/metrics/service/driver/upgrade 与 gopsutil。
deps=$(cd "$ROOT" && go list -deps ./cmd/mynetd 2>/dev/null | grep -E 'mynet_agent/mynet/|shared/config|gopsutil' || true)
bad=""
for d in $deps; do
  case "$d" in
    github.com/mynet-club/mynet_agent/mynet/keys|github.com/mynet-club/mynet_agent/shared/config|github.com/mynet-club/mynet_agent/shared/creds) ;;
    github.com/mynet-club/mynet_agent/mynet/detector|github.com/mynet-club/mynet_agent/mynet/tools|github.com/mynet-club/mynet_agent/mynet/apply|github.com/mynet-club/mynet_agent/mynet/metrics|github.com/mynet-club/mynet_agent/mynet/service|github.com/mynet-club/mynet_agent/mynet/driver|github.com/mynet-club/mynet_agent/mynet/upgrade|*gopsutil*) bad="$bad $d" ;;
  esac
done
if [ -z "$bad" ]; then
  printf '  ✓ cmd/mynetd 无 detector/tools/apply/metrics/service/driver/upgrade 传递依赖\n'
  pass=$((pass+1))
else
  printf '  ✗ cmd/mynetd 仍传递依赖:%s\n' "$bad"
  fail=$((fail+1))
fi

ld=$(cd "$ROOT" && go list -deps ./mynetd/localapi 2>/dev/null | grep -E 'mynet_agent/mynet($|/)|shared/config|gopsutil' || true)
if [ -z "$ld" ]; then
  printf '  ✓ mynetd/localapi 传递依赖干净\n'
  pass=$((pass+1))
else
  printf '  ✗ mynetd/localapi 仍依赖:%s\n' "$ld"
  fail=$((fail+1))
fi

echo "B2 变异测试：$pass 通过 / $fail 失败"
[ "$fail" -eq 0 ] || exit 1
