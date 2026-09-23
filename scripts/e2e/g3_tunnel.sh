#!/bin/sh
# T1-6 / G3：双节点真隧道（非 dry-run）
# 宿主 2001 + OpenWrt VM 2002，经 192.168.101.x 互联
# 用法: sh scripts/e2e/g3_tunnel.sh [--setup|--check|--down]
set -e
T1=/tmp/t1-gnb
GNB_H="${GNB_H:-/Users/arthur/Devel/WorkSpacePHP/lyt.com/mynet_gnb/src/bin}"
VM_SSH="${VM_SSH:-openwrt-qemu}"
VM_GNB="${VM_GNB:-/etc/mynet/driver/gnb/bin}"
fail=0
log() { echo "[G3] $*"; }
ck() { if "$@" >/dev/null 2>&1; then echo "  ✓ $*"; else echo "  ✗ $*"; fail=1; fi; }

check() {
  log "1) VPN IP 互 ping"
  ck sudo ping -c 2 -t 2 10.21.0.2
  ck ssh "$VM_SSH" "ping -c 2 -w 2 10.21.0.1"

  log "2) Direct P2P"
  out=$(ssh "$VM_SSH" "$VM_GNB/gnb_ctl -b $T1/2002/gnb.map -s 2>/dev/null | grep -c 'Direct Point' || true")
  if [ "${out:-0}" -ge 1 ]; then echo "  ✓ Direct P2P"; else echo "  ✗ not Direct"; fail=1; fi

  log "3) 双向流量"
  ssh "$VM_SSH" "ping -c 5 -w 5 10.21.0.1" >/tmp/g3-ping.txt 2>&1 || true
  loss=$(grep -o '[0-9]*% packet loss' /tmp/g3-ping.txt | head -1)
  echo "  · VM→host $loss"

  if [ $fail -eq 0 ]; then
    echo "G3 TUNNEL PASS（gnb 双节点 Direct）"
  else
    echo "G3 FAIL"; exit 1
  fi
}

down() {
  pkill -f "gnb -c $T1/2001" 2>/dev/null || true
  ssh "$VM_SSH" "pkill -f 'gnb -c $T1/2002' 2>/dev/null || true"
  log "down"
}

case "${1:-check}" in
  --check|check) check ;;
  --down|down) down ;;
  *) check ;;
esac
