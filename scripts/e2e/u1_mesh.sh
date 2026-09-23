#!/bin/sh
# U1-A: 多节点 gnb/wg 矩阵
# 节点: A=192.168.0.32  B=192.168.0.16  C=VM(192.168.0.108)
A=192.168.0.32; B=192.168.0.16; C=openwrt-qemu
VPN_A=10.31.0.1; VPN_B=10.31.0.2; VPN_C=10.31.0.3
fail=0
ck() {
  r=$(eval "$2" 2>/dev/null)
  if [ "$r" = "OK" ]; then echo "  ✓ $1"; else echo "  ✗ $1 ($r)"; fail=1; fi
}
echo "=== gnb mesh 10.31.0.0/24 ==="
ck "A→B" "ssh arthur@$A 'ping -c 2 -w 2 $VPN_B >/dev/null && echo OK || echo FAIL'"
ck "A→C" "ssh arthur@$A 'ping -c 2 -w 2 $VPN_C >/dev/null && echo OK || echo FAIL'"
ck "B→A" "ssh arthur@$B 'ping -c 2 -w 2 $VPN_A >/dev/null && echo OK || echo FAIL'"
ck "B→C" "ssh arthur@$B 'ping -c 2 -w 2 $VPN_C >/dev/null && echo OK || echo FAIL'"
ck "C→A" "ssh $C 'ping -c 2 -w 2 $VPN_A >/dev/null && echo OK || echo FAIL'"
ck "C→B" "ssh $C 'ping -c 2 -w 2 $VPN_B >/dev/null && echo OK || echo FAIL'"

if [ "$1" = "--wg" ]; then
  echo "=== wg mesh 10.22.0.0/24 ==="
  ck "A→B wg" "ssh arthur@$A 'ping -c 2 -w 2 10.22.0.2 >/dev/null && echo OK || echo FAIL'"
fi

if [ $fail -eq 0 ]; then echo "U1 MESH PASS"; else echo "U1 MESH FAIL"; exit 1; fi
