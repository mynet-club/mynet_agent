#!/bin/sh
# R1-6 / G1：gnb · wg · 跨驱动 三场景出口脚本
# 领域层已在 DualDriverBusinessTest / A8 覆盖；这里串执行器 + apply 形状
set -e
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
cd "$ROOT"
go build -o /tmp/g1-mynet ./cmd/mynet
export MYNET_HOME=$(mktemp -d)
mkdir -p "$MYNET_HOME/var"
fail=0
check() {
  if "$@"; then echo "  ✓ $*"; else echo "  ✗ $*"; fail=1; fi
}

echo "== 场景 A: gnb =="
cat > "$MYNET_HOME/var/bundle.json" <<'JSON'
{"schema_version":2,"generation":"g","checksum":"sha256:0","generated_at":"2026-09-23T00:00:00Z",
 "node":{"id":"1","interface":"gnb0"},"driver":"gnb",
 "network":{"routes":[{"prefix":"10.8.0.0/24","dev":"gnb0"}]},
 "gnb":{"identity":{"node_id":"1","key_algorithm":"ed25519"},"listeners":["0.0.0.0:9000"]},
 "wireguard":null}
JSON
check /tmp/g1-mynet refresh-config --auto
check /tmp/g1-mynet refresh-routes --auto

echo "== 场景 B: wireguard =="
cat > "$MYNET_HOME/var/bundle.json" <<'JSON'
{"schema_version":2,"generation":"g","checksum":"sha256:0","generated_at":"2026-09-23T00:00:00Z",
 "node":{"id":"2","interface":"wg0","listen_port":51820},"driver":"wireguard",
 "network":{"routes":[{"prefix":"10.9.0.0/24","dev":"wg0"}]},
 "gnb":null,
 "wireguard":{"interface":{"private_key":"K","public_key":"P","key_algorithm":"curve25519","listen_port":51820,"fwmark":null},
  "peers":[{"public_key":"Q","allowed_ips":["10.9.0.2/32"],"preshared_key":null,"bootstrap_endpoint":null}]}}
JSON
check /tmp/g1-mynet refresh-config --auto
check /tmp/g1-mynet refresh-routes --auto

echo "== 场景 C: 跨驱动 =="
cat > "$MYNET_HOME/var/plan.json" <<'JSON'
{"routes":[
  {"action":"replace","destination":"10.8.0.0/24","device":"gnb0","metric":100},
  {"action":"replace","destination":"10.9.0.0/24","device":"wg0","metric":200}
]}
JSON
/tmp/g1-mynet detector --force >/dev/null
if /tmp/g1-mynet apply --dry-run "$MYNET_HOME/var/plan.json" | grep -q 'ip route replace 10.8.0.0/24 dev gnb0'; then
  echo "  ✓ 跨驱动 gnb0 路由"
else
  echo "  ✗ 跨驱动 gnb0 路由"; fail=1
fi
if /tmp/g1-mynet apply --dry-run "$MYNET_HOME/var/plan.json" | grep -q 'ip route replace 10.9.0.0/24 dev wg0'; then
  echo "  ✓ 跨驱动 wg0 路由"
else
  echo "  ✗ 跨驱动 wg0 路由"; fail=1
fi

if [ $fail -eq 0 ]; then
  echo "G1 三场景 PASS"
else
  echo "G1 FAIL"; exit 1
fi
