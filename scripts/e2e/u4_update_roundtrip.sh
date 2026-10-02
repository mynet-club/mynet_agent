#!/usr/bin/env bash
# U4 真升级一轮：本地静态索引 + 真 mynet 二进制，验证
#   下载 → sha256 校验 → 备份 → 原子替换 → 自检 → （负路径）校验和不符拒绝
#
# 用法: bash scripts/e2e/u4_update_roundtrip.sh
# 退出码: 0=全过
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

PASS=0; FAIL=0
ok()   { echo "  ✓ $*"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $*"; FAIL=$((FAIL+1)); }
note() { echo "  · $*"; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "== U4 真升级一轮 =="

# 1. 构建两个版本的 mynet（只测自更新组件）
echo "-- 构建 v0.0.1 / v0.0.2"
mkdir -p "$WORK/v1" "$WORK/v2"
(cd "$ROOT" && go build -ldflags '-X github.com/mynet-club/mynet_agent/mynet.Version=0.0.1' \
  -o "$WORK/v1/mynet" ./cmd/mynet)
(cd "$ROOT" && go build -ldflags '-X github.com/mynet-club/mynet_agent/mynet.Version=0.0.2' \
  -o "$WORK/v2/mynet" ./cmd/mynet)
# 归档里还要 mynetd / mynet_gateway 占位（Install 认 binaryNames）
echo "fake-mynetd-v1" > "$WORK/v1/mynetd"; echo "fake-gw-v1" > "$WORK/v1/mynet_gateway"
echo "fake-mynetd-v2" > "$WORK/v2/mynetd"; echo "fake-gw-v2" > "$WORK/v2/mynet_gateway"

pack() {
  local src="$1" top="$2" out="$3"
  (cd "$(dirname "$src")" && tar czf "$out" "$top")
}
# 归档结构：顶层目录 + 三个二进制（与 pack.sh 同构）
mkdir -p "$WORK/pkg1/mynet_agent_v0.0.1" "$WORK/pkg2/mynet_agent_v0.0.2"
cp "$WORK/v1/"* "$WORK/pkg1/mynet_agent_v0.0.1/"
cp "$WORK/v2/"* "$WORK/pkg2/mynet_agent_v0.0.2/"
(cd "$WORK/pkg1" && tar czf "$WORK/mynet_agent_v0.0.1.tgz" mynet_agent_v0.0.1)
(cd "$WORK/pkg2" && tar czf "$WORK/mynet_agent_v0.0.2.tgz" mynet_agent_v0.0.2)

SUM1=$(shasum -a 256 "$WORK/mynet_agent_v0.0.1.tgz" | awk '{print $1}')
SUM2=$(shasum -a 256 "$WORK/mynet_agent_v0.0.2.tgz" | awk '{print $1}')
ok "归档就绪 sum1=${SUM1:0:12}… sum2=${SUM2:0:12}…"

# 2. 本地索引（两步协议：apps.json → manifest）
cat > "$WORK/apps.json" <<EOF
{
  "schema_version": "2",
  "products": {
    "mynet_agent": {
      "name": "mynet_agent",
      "description": "MyNet agent",
      "latest": "0.0.2",
      "manifest_url": "/d/mynet_agent/manifest.json"
    }
  }
}
EOF
cat > "$WORK/manifest.json" <<EOF
{
  "schema_version": "2",
  "project": "mynet_agent",
  "latest_stable": "0.0.2",
  "versions": {
    "0.0.1": {
      "channel": "stable",
      "artifacts": [{
        "os": "darwin", "arch": "arm64",
        "file": "mynet_agent_v0.0.1.tgz", "ext": "tgz",
        "url": "http://127.0.0.1:PORT/mynet_agent_v0.0.1.tgz",
        "size": 0, "sha256": "$SUM1"
      }]
    },
    "0.0.2": {
      "channel": "stable",
      "artifacts": [{
        "os": "darwin", "arch": "arm64",
        "file": "mynet_agent_v0.0.2.tgz", "ext": "tgz",
        "url": "http://127.0.0.1:PORT/mynet_agent_v0.0.2.tgz",
        "size": 0, "sha256": "$SUM2"
      }]
    }
  }
}
EOF

PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
sed -i '' "s/PORT/$PORT/g" "$WORK/manifest.json" 2>/dev/null || sed -i "s/PORT/$PORT/g" "$WORK/manifest.json"
mkdir -p "$WORK/d/mynet_agent"
cp "$WORK/manifest.json" "$WORK/d/mynet_agent/manifest.json"
# 两步协议：apps.json 在 /d/apps.json（index.go 注释：GET {base}/d/apps.json）
cp "$WORK/apps.json" "$WORK/d/apps.json"

python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$WORK" >/dev/null 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null; rm -rf "$WORK"' EXIT
sleep 0.5

BASE="http://127.0.0.1:$PORT"
export MYNET_APPS_INDEX="$BASE"
note "索引: $BASE"

# 3. 安装 v0.0.1 到「安装目录」
BIN="$WORK/bin"
mkdir -p "$BIN"
cp "$WORK/v1/mynet" "$BIN/mynet"
cp "$WORK/v1/mynetd" "$BIN/mynetd"
cp "$WORK/v1/mynet_gateway" "$BIN/mynet_gateway"
chmod +x "$BIN/mynet"

CUR=$("$BIN/mynet" version | head -1)
if [ "$CUR" = "0.0.1" ]; then ok "起点版本 $CUR"; else bad "起点版本应 0.0.1，实得 $CUR"; fi

# 4. dry-run：应打印计划、不动文件
echo "-- dry-run"
DRC=$("$BIN/mynet" update --dry-run 2>&1 | tee "$WORK/dry.out" | tail -1; echo "exit:$?")
grep -q "0.0.2" "$WORK/dry.out" && ok "dry-run 指出目标 0.0.2" || bad "dry-run 未提 0.0.2"
grep -q "sha256" "$WORK/dry.out" && ok "dry-run 含校验和计划" || bad "dry-run 缺校验和"
grep -q "回滚" "$WORK/dry.out" && ok "dry-run 含回滚步骤" || bad "dry-run 缺回滚"
NEW=$("$BIN/mynet" version | head -1)
if [ "$NEW" = "0.0.1" ]; then ok "dry-run 未替换二进制"; else bad "dry-run 不应改版本，实得 $NEW"; fi

# 5. --check：有新版本应退出 10
echo "-- check"
set +e
"$BIN/mynet" update --check >/dev/null 2>&1
CHECK_RC=$?
set -e
if [ "$CHECK_RC" = "10" ]; then ok "--check 有新版本退出 10"; else bad "--check 退出码 ${CHECK_RC}（应 10）"; fi

# 6. 真升级
echo "-- 真升级"
"$BIN/mynet" update 2>&1 | tail -3
NEW=$("$BIN/mynet" version | head -1)
if [ "$NEW" = "0.0.2" ]; then ok "升级后版本 $NEW"; else bad "升级后应 0.0.2，实得 $NEW"; fi
if [ -f "$BIN/mynet.bak" ]; then ok "备份 mynet.bak 存在（默认保留）"; else note "无 .bak（RemoveBackup 已开或未备份）"; fi
if grep -q "fake-mynetd-v2" "$BIN/mynetd"; then ok "mynetd 同步替换"; else bad "mynetd 未替换"; fi

# 7. 负路径：篡改校验和必须拒绝
echo "-- 负路径（校验和不符）"
python3 - "$WORK/manifest.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p))
d["versions"]["0.0.2"]["artifacts"][0]["sha256"]="0"*64
json.dump(d,open(p,"w"))
PY
cp "$WORK/manifest.json" "$WORK/d/mynet_agent/manifest.json"
# 先装回 0.0.1 再试
cp "$WORK/v1/mynet" "$BIN/mynet"; cp "$WORK/v1/mynetd" "$BIN/mynetd"
set +e
OUT=$("$BIN/mynet" update 2>&1)
NEG_RC=$?
set -e
if [ "$NEG_RC" != "0" ]; then ok "校验和不符被拒（exit ${NEG_RC}）"; else bad "篡改校验和竟成功"; fi
echo "$OUT" | grep -qi "sha256" && ok "错误信息提到 sha256" || note "错误信息: $OUT"
NEW=$("$BIN/mynet" version | head -1)
if [ "$NEW" = "0.0.1" ]; then ok "拒绝后二进制未动"; else bad "拒绝后版本被改: $NEW"; fi

echo
echo "== U4 结果: $PASS 通过 / $FAIL 失败 =="
[ "$FAIL" = "0" ]
