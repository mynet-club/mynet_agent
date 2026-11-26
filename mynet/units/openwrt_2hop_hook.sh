#!/bin/sh
# MyNet 两跳中转运行时钩子（role both：既做出入口又用下一跳出口）。
#
# 做的事：仅把“从 tun 进入的客户流量”打标，经 ip rule → table 200 回灌 tun，
# 由 gnb proxy client 按 proxy.conf 选下一跳出口；overlay/LAN 目的不打标。
#
# ACL（与产品口径一致：默认 open；黑名单按 CIDR 可执行）：
#   优先读 $MYNET_HOME/var/2hop_acl.json（agent 按云端 ACL 派生）
#   其次读 $MYNET_HOME/conf/local/2hop_acl.json（本地手工覆盖）
#   格式：{"schema":1,"mode":"open|allowlist|denylist","allow_cidrs":[],"deny_cidrs":[]}
#
# 幂等，可重复执行（procd post_start 钩子与 fw3 include 共用同一份）。
# 关闭：MYNET_2HOP_DISABLE=1 执行一次，清规则并退出。
set -u
MYNET_HOME="${MYNET_HOME:-/etc/mynet}"
DEV="${MYNET_2HOP_DEV:-gnb_tun}"
LAN="${MYNET_2HOP_LAN:-192.168.100.0/24}"
OVL="${MYNET_2HOP_OVL:-10.182.236.0/24}"
MARK=0xc8
TABLE=200
PRIO=200

cleanup() {
	iptables -t mangle -D PREROUTING -i "$DEV" -j MYNET_2HOP_SRC 2>/dev/null
	iptables -t mangle -D PREROUTING -i "$DEV" -s 10.182.236.181/32 -j MYNET_2HOP 2>/dev/null
	for c in MYNET_2HOP_SRC MYNET_2HOP_MARK MYNET_2HOP; do
		iptables -t mangle -F "$c" 2>/dev/null
		iptables -t mangle -X "$c" 2>/dev/null
	done
	ip rule del fwmark $MARK lookup $TABLE prio $PRIO 2>/dev/null
	ip route flush table $TABLE 2>/dev/null
}

if [ "${MYNET_2HOP_DISABLE:-0}" = "1" ]; then
	cleanup
	echo "2hop: disabled (rules removed)"
	exit 0
fi

CONF="${MYNET_2HOP_ACL:-}"
if [ -z "$CONF" ]; then
	if [ -f "$MYNET_HOME/var/2hop_acl.json" ]; then
		CONF="$MYNET_HOME/var/2hop_acl.json"
	elif [ -f "$MYNET_HOME/conf/local/2hop_acl.json" ]; then
		CONF="$MYNET_HOME/conf/local/2hop_acl.json"
	fi
fi

MODE=open
ALLOW=""
DENY=""
if [ -n "$CONF" ] && [ -f "$CONF" ] && command -v jsonfilter >/dev/null 2>&1; then
	MODE=$(jsonfilter -i "$CONF" -e '@.mode' 2>/dev/null) || MODE=open
	ALLOW=$(jsonfilter -i "$CONF" -e '@.allow_cidrs[*]' 2>/dev/null)
	DENY=$(jsonfilter -i "$CONF" -e '@.deny_cidrs[*]' 2>/dev/null)
fi
case "$MODE" in
	open|allowlist|denylist) ;;
	*) MODE=open ;;
esac

ip rule del fwmark $MARK lookup $TABLE prio $PRIO 2>/dev/null
ip rule add fwmark $MARK lookup $TABLE prio $PRIO
ip route replace default dev "$DEV" table $TABLE 2>/dev/null || true

iptables -t mangle -N MYNET_2HOP_MARK 2>/dev/null
iptables -t mangle -F MYNET_2HOP_MARK
iptables -t mangle -A MYNET_2HOP_MARK -d "$OVL" -j RETURN
iptables -t mangle -A MYNET_2HOP_MARK -d "$LAN" -j RETURN
iptables -t mangle -A MYNET_2HOP_MARK -j MARK --set-xmark ${MARK}/0xffffffff

iptables -t mangle -N MYNET_2HOP_SRC 2>/dev/null
iptables -t mangle -F MYNET_2HOP_SRC
for c in $DENY; do
	iptables -t mangle -A MYNET_2HOP_SRC -s "$c" -j RETURN 2>/dev/null || true
done
if [ "$MODE" = "allowlist" ]; then
	for c in $ALLOW; do
		iptables -t mangle -A MYNET_2HOP_SRC -s "$c" -j MYNET_2HOP_MARK 2>/dev/null || true
	done
	iptables -t mangle -A MYNET_2HOP_SRC -j RETURN
else
	iptables -t mangle -A MYNET_2HOP_SRC -j MYNET_2HOP_MARK
fi

iptables -t mangle -D PREROUTING -i "$DEV" -j MYNET_2HOP_SRC 2>/dev/null
iptables -t mangle -I PREROUTING 1 -i "$DEV" -j MYNET_2HOP_SRC
echo "2hop: mode=$MODE allow=[$ALLOW] deny=[$DENY]"
