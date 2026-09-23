#!/bin/sh
set -e
AGENT=$(cd "$(dirname "$0")/.." && pwd)
cd "$AGENT"
STAGE=$(mktemp -d)
fail=0
log() { echo "[G2] $*"; }
log "1) dist"; make dist >/dev/null 2>&1 || true
log "2) 冷装"
go build -o /tmp/g2-mynet ./cmd/mynet
/tmp/g2-mynet install --prefix "$STAGE/opt" >/dev/null 2>&1 || true
[ -f "$STAGE/opt/bin/mynet" ] && log "  ok installed" || { log "  fail install"; fail=1; }
log "3) 业务"
export MYNET_HOME="$STAGE/home"
mkdir -p "$MYNET_HOME/var"
"$STAGE/opt/bin/mynet" detector --force >/dev/null 2>&1 || true
"$STAGE/opt/bin/mynet" setup --print-keys >/dev/null
echo '{"routes":[{"action":"replace","destination":"10.8.0.0/24","device":"wg0"}]}' > "$MYNET_HOME/var/plan.json"
"$STAGE/opt/bin/mynet" apply --dry-run "$MYNET_HOME/var/plan.json" | grep -q 'ip route' && log "  ok apply" || { log "  fail apply"; fail=1; }
[ $fail -eq 0 ] && echo "G2 SMOKE PASS" || { echo "G2 FAIL"; exit 1; }
